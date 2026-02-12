package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"ljightningparking/parking"
	"ljightningparking/price"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// Config holds application configuration
type Config struct {
	LNBitsURL       string
	LNBitsAPIKey    string
	CallbackURL     string
	CallbackAPIKey  string
	SMSNumber       string
}

var config Config

// SubmitRequest represents the incoming form data
type SubmitRequest struct {
	Plate string `json:"plate" binding:"required"`
	Zone  string `json:"zone" binding:"required"`
	Hours int64  `json:"hours" binding:"required,min=1"`
}

// LNBitsInvoiceRequest represents the request to create an invoice
type LNBitsInvoiceRequest struct {
	Out     bool   `json:"out"`
	Amount  int64  `json:"amount"`
	Memo    string `json:"memo"`
	Webhook string `json:"webhook,omitempty"`
}

// LNBitsInvoiceResponse represents the response from LNbits
type LNBitsInvoiceResponse struct {
	PaymentHash    string `json:"payment_hash"`
	PaymentRequest string `json:"payment_request"`
}

// LNBitsCheckResponse represents the payment check response
type LNBitsCheckResponse struct {
	Paid bool `json:"paid"`
}

// SMSRequest represents the payload sent to the SMS server
type SMSRequest struct {
	Number  string `json:"number"`
	Content string `json:"content"`
}

// SMSSearchResult represents a received SMS from the search endpoint
type SMSSearchResult struct {
	Content string `json:"content"`
}

func main() {
	// Load configuration from environment variables
	config = Config{
		LNBitsURL:      getEnv("LNBITS_URL", "https://legend.lnbits.com"),
		LNBitsAPIKey:   getEnv("LNBITS_API_KEY", ""),
		CallbackURL:    getEnv("CALLBACK_URL", ""),
		CallbackAPIKey: getEnv("CALLBACK_API_KEY", ""),
		SMSNumber:      getEnv("SMS_NUMBER", ""),
	}

	if config.LNBitsAPIKey == "" {
		log.Fatal("LNBITS_API_KEY environment variable is required")
	}

	if config.CallbackURL == "" {
		log.Fatal("CALLBACK_URL environment variable is required")
	}

	if config.SMSNumber == "" {
		log.Fatal("SMS_NUMBER environment variable is required")
	}

	router := gin.Default()
	router.SetTrustedProxies(nil)

	// Serve the main page
	router.GET("/", handleMainPage)

	// Handle form submission
	router.POST("/submit", handleSubmit)

	// Check payment status
	router.GET("/check-payment/:payment_hash", handleCheckPayment)

	// Check for SMS confirmation
	router.GET("/check-sms", handleCheckSMS)

	// Wake up SMS server GSM module
	router.POST("/wakeup", handleWakeup)

	// Legal pages
	router.GET("/privacy", handlePrivacyPage)
	router.GET("/terms", handleTermsPage)

	port := getEnv("PORT", "8080")
	log.Printf("Starting server on port %s", port)
	router.Run(":" + port)
}

func handleMainPage(c *gin.Context) {
	// Get all zones and sort them
	zones := make([]string, 0, len(parking.Zones))
	for zoneName := range parking.Zones {
		zones = append(zones, zoneName)
	}
	sort.Strings(zones)

	html := generateHTML(zones)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func handleSubmit(c *gin.Context) {
	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate zone
	zone, ok := parking.Zones[req.Zone]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid parking zone"})
		return
	}

	// Validate plate (basic validation)
	if len(req.Plate) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid license plate"})
		return
	}

	// Calculate parking fee in euros
	feeEuros := zone.GetParkingFee(req.Hours)

	// Convert to satoshis
	feeSatoshis := price.EuroToSatoshis(feeEuros)
	if feeSatoshis <= 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get Bitcoin price"})
		return
	}

	// Create invoice
	memo := fmt.Sprintf("Parking: %s @ %s for %d hours (%.2f EUR)", req.Plate, req.Zone, req.Hours, feeEuros)
	invoice, err := createLNBitsInvoice(feeSatoshis, memo)
	if err != nil {
		log.Printf("Failed to create invoice: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Lightning invoice"})
		return
	}

	// Store payment info in memory (in production, use a database)
	// For now, we'll pass it through the frontend
	c.JSON(http.StatusOK, gin.H{
		"payment_hash":    invoice.PaymentHash,
		"payment_request": invoice.PaymentRequest,
		"amount_eur":      feeEuros,
		"amount_sats":     feeSatoshis,
		"plate":           req.Plate,
		"zone":            req.Zone,
		"hours":           req.Hours,
	})
}

func handleCheckPayment(c *gin.Context) {
	paymentHash := c.Param("payment_hash")

	paid, err := checkLNBitsPayment(paymentHash)
	if err != nil {
		log.Printf("Failed to check payment: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check payment status"})
		return
	}

	if paid {
		// Get parking data from query params
		plate := c.Query("plate")
		zone := c.Query("zone")
		hours := c.Query("hours")
		amount := c.Query("amount")

		smsSentAt := time.Now().UTC().Format(time.RFC3339)

		if plate != "" && zone != "" && hours != "" {
			err := sendToParkingServer(plate, zone, hours, amount)
			if err != nil {
				log.Printf("Failed to send data to parking server: %v", err)
			} else {
				log.Printf("Successfully sent parking data for plate %s", plate)
			}
		}

		c.JSON(http.StatusOK, gin.H{"paid": true, "sms_sent_at": smsSentAt})
		return
	}

	c.JSON(http.StatusOK, gin.H{"paid": false})
}

func handleCheckSMS(c *gin.Context) {
	plate := c.Query("plate")
	after := c.Query("after")

	if plate == "" || after == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plate and after are required"})
		return
	}

	results, err := searchReceivedSMS(plate, after)
	if err != nil {
		log.Printf("Failed to search received SMS: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check SMS"})
		return
	}

	if len(results) > 0 {
		c.JSON(http.StatusOK, gin.H{"found": true, "content": results[0].Content})
	} else {
		c.JSON(http.StatusOK, gin.H{"found": false})
	}
}

func handleWakeup(c *gin.Context) {
	go func() {
		err := wakeupSMSServer()
		if err != nil {
			log.Printf("Failed to wake up SMS server: %v", err)
		} else {
			log.Printf("SMS server wakeup sent")
		}
	}()

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func wakeupSMSServer() error {
	u, err := url.Parse(config.CallbackURL)
	if err != nil {
		return fmt.Errorf("invalid callback URL: %v", err)
	}

	wakeupURL := fmt.Sprintf("%s://%s/wakeup", u.Scheme, u.Host)

	req, err := http.NewRequest("GET", wakeupURL, nil)
	if err != nil {
		return err
	}

	if config.CallbackAPIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.CallbackAPIKey))
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wakeup error: %s - %s", resp.Status, string(body))
	}

	return nil
}

func searchReceivedSMS(plate, after string) ([]SMSSearchResult, error) {
	u, err := url.Parse(config.CallbackURL)
	if err != nil {
		return nil, fmt.Errorf("invalid callback URL: %v", err)
	}

	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	searchURL := fmt.Sprintf("%s/received/search?q=%s&after=%s", baseURL, url.QueryEscape(plate), url.QueryEscape(after))

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	if config.CallbackAPIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.CallbackAPIKey))
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("SMS server error: %s - %s", resp.Status, string(body))
	}

	var results []SMSSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}

	return results, nil
}

func createLNBitsInvoice(amountSats int64, memo string) (*LNBitsInvoiceResponse, error) {
	reqBody := LNBitsInvoiceRequest{
		Out:    false,
		Amount: amountSats,
		Memo:   memo,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/v1/payments", config.LNBitsURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", config.LNBitsAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LNbits API error: %s - %s", resp.Status, string(body))
	}

	var invoice LNBitsInvoiceResponse
	if err := json.NewDecoder(resp.Body).Decode(&invoice); err != nil {
		return nil, err
	}

	return &invoice, nil
}

func checkLNBitsPayment(paymentHash string) (bool, error) {
	url := fmt.Sprintf("%s/api/v1/payments/%s", config.LNBitsURL, paymentHash)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("X-Api-Key", config.LNBitsAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("LNbits API error: %s", resp.Status)
	}

	var checkResp LNBitsCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return false, err
	}

	return checkResp.Paid, nil
}

func sendToParkingServer(plate, zone, hours, amount string) error {
	data := SMSRequest{
		Number:  config.SMSNumber,
		Content: fmt.Sprintf("%s %s %s", zone, plate, hours),
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", config.CallbackURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if config.CallbackAPIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.CallbackAPIKey))
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("parking server error: %s - %s", resp.Status, string(body))
	}

	return nil
}

func handlePrivacyPage(c *gin.Context) {
	html := generatePrivacyHTML()
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func handleTermsPage(c *gin.Context) {
	html := generateTermsHTML()
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func generateHTML(zones []string) string {
	zoneOptions := ""
	for _, zone := range zones {
		zoneInfo := parking.Zones[zone]
		zoneOptions += fmt.Sprintf(`<option value="%s">%s - €%.2f/hr (max %vh)</option>`,
			zone, zone, zoneInfo.Price, int(zoneInfo.MaxTime))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Lightning Parking Payment</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            padding: 20px;
        }

        .container {
            background: white;
            border-radius: 20px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 500px;
            width: 100%%;
        }

        h1 {
            color: #333;
            margin-bottom: 10px;
            font-size: 28px;
            text-align: center;
        }

        .subtitle {
            color: #666;
            text-align: center;
            margin-bottom: 30px;
            font-size: 14px;
        }

        .form-group {
            margin-bottom: 25px;
        }

        label {
            display: block;
            margin-bottom: 8px;
            color: #333;
            font-weight: 600;
            font-size: 14px;
        }

        input[type="text"],
        select {
            width: 100%%;
            padding: 12px 15px;
            border: 2px solid #e0e0e0;
            border-radius: 10px;
            font-size: 16px;
            transition: border-color 0.3s;
        }

        input[type="text"]:focus,
        select:focus {
            outline: none;
            border-color: #667eea;
        }

        .number-input {
            display: flex;
            align-items: center;
            gap: 15px;
        }

        .number-input button {
            width: 45px;
            height: 45px;
            border: 2px solid #667eea;
            background: white;
            color: #667eea;
            border-radius: 10px;
            font-size: 20px;
            font-weight: bold;
            cursor: pointer;
            transition: all 0.3s;
        }

        .number-input button:hover:not(:disabled) {
            background: #667eea;
            color: white;
        }

        .number-input button:disabled {
            opacity: 0.3;
            cursor: not-allowed;
        }

        .number-input input {
            flex: 1;
            text-align: center;
            font-weight: bold;
            font-size: 18px;
        }

        .submit-btn {
            width: 100%%;
            padding: 15px;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: white;
            border: none;
            border-radius: 10px;
            font-size: 18px;
            font-weight: bold;
            cursor: pointer;
            transition: transform 0.2s, box-shadow 0.2s;
        }

        .submit-btn:hover {
            transform: translateY(-2px);
            box-shadow: 0 5px 20px rgba(102, 126, 234, 0.4);
        }

        .submit-btn:active {
            transform: translateY(0);
        }

        .submit-btn:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
        }

        /* Modal styles */
        .modal {
            display: none;
            position: fixed;
            z-index: 1000;
            left: 0;
            top: 0;
            width: 100%%;
            height: 100%%;
            background-color: rgba(0, 0, 0, 0.7);
            animation: fadeIn 0.3s;
        }

        @keyframes fadeIn {
            from { opacity: 0; }
            to { opacity: 1; }
        }

        .modal-content {
            background-color: white;
            margin: 5%% auto;
            padding: 30px;
            border-radius: 20px;
            max-width: 500px;
            width: 90%%;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            animation: slideIn 0.3s;
        }

        @keyframes slideIn {
            from {
                transform: translateY(-50px);
                opacity: 0;
            }
            to {
                transform: translateY(0);
                opacity: 1;
            }
        }

        .modal h2 {
            color: #333;
            margin-bottom: 20px;
            text-align: center;
        }

        .invoice-container {
            background: #f5f5f5;
            padding: 20px;
            border-radius: 10px;
            margin: 20px 0;
            word-break: break-all;
        }

        .invoice-details {
            margin-bottom: 15px;
            padding: 15px;
            background: white;
            border-radius: 10px;
        }

        .invoice-details p {
            margin: 8px 0;
            color: #666;
        }

        .invoice-details strong {
            color: #333;
        }

        .qr-code {
            text-align: center;
            margin: 20px 0;
        }

        .qr-code img {
            max-width: 256px;
            width: 100%%;
        }

        .copy-btn {
            width: 100%%;
            padding: 12px;
            background: #667eea;
            color: white;
            border: none;
            border-radius: 10px;
            font-size: 14px;
            cursor: pointer;
            margin-top: 10px;
        }

        .copy-btn:hover {
            background: #5568d3;
        }

        .loading {
            text-align: center;
            color: #667eea;
            font-weight: bold;
            margin: 15px 0;
        }

        .spinner {
            border: 3px solid #f3f3f3;
            border-top: 3px solid #667eea;
            border-radius: 50%%;
            width: 40px;
            height: 40px;
            animation: spin 1s linear infinite;
            margin: 20px auto;
        }

        @keyframes spin {
            0%% { transform: rotate(0deg); }
            100%% { transform: rotate(360deg); }
        }

        .success-message {
            background: #4CAF50;
            color: white;
            padding: 20px;
            border-radius: 10px;
            text-align: center;
            margin: 20px 0;
        }

        .error {
            color: #f44336;
            font-size: 14px;
            margin-top: 10px;
            text-align: center;
        }

        .footer {
            margin-top: 25px;
            padding-top: 20px;
            border-top: 1px solid #e0e0e0;
            text-align: center;
            font-size: 12px;
            color: #888;
        }

        .footer a {
            color: #667eea;
            text-decoration: none;
            margin: 0 10px;
        }

        .footer a:hover {
            text-decoration: underline;
        }

        /* Mobile styles */
        @media (max-width: 768px) {
            body {
                padding: 0;
                align-items: flex-start;
            }

            .container {
                border-radius: 0;
                padding: 15px;
                box-shadow: none;
            }

            .modal-content {
                margin: 0;
                border-radius: 0;
                width: 100%%;
                max-width: 100%%;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>⚡ Lightning Parking</h1>
        <p class="subtitle">Pay for your parking with Bitcoin</p>

        <form id="parkingForm">
            <div class="form-group">
                <label for="plate">License Plate</label>
                <input type="text" id="plate" name="plate" required placeholder="Enter your license plate">
            </div>

            <div class="form-group">
                <label for="zone">Parking Zone</label>
                <select id="zone" name="zone" required>
                    <option value="">Select a zone</option>
                    %s
                </select>
            </div>

            <div class="form-group">
                <label for="hours">Hours</label>
                <div class="number-input">
                    <button type="button" id="decreaseHours">−</button>
                    <input type="number" id="hours" name="hours" value="1" min="1" max="24" readonly>
                    <button type="button" id="increaseHours">+</button>
                </div>
            </div>

            <button type="submit" class="submit-btn" id="submitBtn">Generate Invoice</button>
            <div id="formError" class="error"></div>
        </form>

        <div class="footer">
            <a href="/privacy">Privacy Policy</a>
            <a href="/terms">Terms of Use</a>
        </div>
    </div>

    <!-- Invoice Modal -->
    <div id="invoiceModal" class="modal">
        <div class="modal-content">
            <h2>⚡ Pay with Lightning</h2>
            <div class="invoice-details" id="invoiceDetails"></div>
            <div class="qr-code" id="qrCode"></div>
            <div class="invoice-container" id="invoiceContainer"></div>
            <div class="loading" id="paymentStatus">
                <div class="spinner"></div>
                <p>Waiting for payment...</p>
            </div>
            <div id="successMessage" style="display: none;" class="success-message">
                <h3>✅ Payment Successful!</h3>
                <p>Your parking has been activated.</p>
            </div>
        </div>
    </div>

    <script>
        const form = document.getElementById('parkingForm');
        const hoursInput = document.getElementById('hours');
        const decreaseBtn = document.getElementById('decreaseHours');
        const increaseBtn = document.getElementById('increaseHours');
        const modal = document.getElementById('invoiceModal');
        const submitBtn = document.getElementById('submitBtn');
        const formError = document.getElementById('formError');

        let checkPaymentInterval;
        let checkSMSInterval;
        let currentPaymentHash;
        let currentParkingData;
        let wakeupSent = false;

        // Wake up SMS server on first interaction with plate or zone
        function triggerWakeup() {
            if (wakeupSent) return;
            wakeupSent = true;
            fetch('/wakeup', { method: 'POST' }).catch(() => {});
        }
        document.getElementById('plate').addEventListener('input', triggerWakeup);
        document.getElementById('zone').addEventListener('change', triggerWakeup);

        // Hours increment/decrement
        decreaseBtn.addEventListener('click', () => {
            const current = parseInt(hoursInput.value);
            if (current > 1) {
                hoursInput.value = current - 1;
            }
        });

        increaseBtn.addEventListener('click', () => {
            const current = parseInt(hoursInput.value);
            if (current < 24) {
                hoursInput.value = current + 1;
            }
        });

        // Form submission
        form.addEventListener('submit', async (e) => {
            e.preventDefault();
            formError.textContent = '';
            submitBtn.disabled = true;
            submitBtn.textContent = 'Processing...';

            const formData = {
                plate: document.getElementById('plate').value.trim(),
                zone: document.getElementById('zone').value,
                hours: parseInt(hoursInput.value)
            };

            try {
                const response = await fetch('/submit', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(formData)
                });

                const data = await response.json();

                if (!response.ok) {
                    throw new Error(data.error || 'Failed to generate invoice');
                }

                // Store parking data for payment check
                currentParkingData = {
                    plate: data.plate,
                    zone: data.zone,
                    hours: data.hours,
                    amount: data.amount_eur
                };

                showInvoice(data);
            } catch (error) {
                formError.textContent = error.message;
            } finally {
                submitBtn.disabled = false;
                submitBtn.textContent = 'Generate Invoice';
            }
        });

        function showInvoice(data) {
            currentPaymentHash = data.payment_hash;

            // Show invoice details
            document.getElementById('invoiceDetails').innerHTML =
                '<p><strong>Amount:</strong> €' + data.amount_eur.toFixed(2) + ' (' + data.amount_sats.toLocaleString() + ' sats)</p>' +
                '<p><strong>Zone:</strong> ' + data.zone + '</p>' +
                '<p><strong>Duration:</strong> ' + data.hours + ' hour(s)</p>' +
                '<p><strong>Plate:</strong> ' + data.plate + '</p>';

            // Generate QR code
            const qrUrl = 'https://api.qrserver.com/v1/create-qr-code/?size=256x256&data=' +
                          encodeURIComponent(data.payment_request);
            document.getElementById('qrCode').innerHTML =
                '<img src="' + qrUrl + '" alt="Invoice QR Code">';

            // Show invoice text with copy button
            document.getElementById('invoiceContainer').innerHTML =
                '<div style="font-size: 12px; font-family: monospace;">' + data.payment_request + '</div>' +
                '<button class="copy-btn" onclick="copyInvoice(\'' + data.payment_request + '\')">Copy Invoice</button>';

            // Show modal
            modal.style.display = 'block';

            // Start checking payment status
            startPaymentCheck();
        }

        function copyInvoice(invoice) {
            navigator.clipboard.writeText(invoice).then(() => {
                alert('Invoice copied to clipboard!');
            });
        }

        function startPaymentCheck() {
            checkPaymentInterval = setInterval(async () => {
                try {
                    const params = new URLSearchParams({
                        plate: currentParkingData.plate,
                        zone: currentParkingData.zone,
                        hours: currentParkingData.hours.toString(),
                        amount: currentParkingData.amount.toString()
                    });

                    const response = await fetch('/check-payment/' + currentPaymentHash + '?' + params);
                    const data = await response.json();

                    if (data.paid) {
                        clearInterval(checkPaymentInterval);
                        document.getElementById('paymentStatus').innerHTML =
                            '<div class="spinner"></div><p>Payment received! Confirming parking...</p>';
                        startSMSCheck(currentParkingData.plate, data.sms_sent_at);
                    }
                } catch (error) {
                    console.error('Error checking payment:', error);
                }
            }, 2000); // Check every 2 seconds
        }

        function startSMSCheck(plate, after) {
            checkSMSInterval = setInterval(async () => {
                try {
                    const params = new URLSearchParams({
                        plate: plate,
                        after: after
                    });
                    const response = await fetch('/check-sms?' + params);
                    const data = await response.json();

                    if (data.found) {
                        clearInterval(checkSMSInterval);
                        showSuccess(data.content);
                    }
                } catch (error) {
                    console.error('Error checking SMS:', error);
                }
            }, 3000);
        }

        function showSuccess(smsContent) {
            document.getElementById('paymentStatus').style.display = 'none';
            const successEl = document.getElementById('successMessage');
            let html = '<h3>✅ Parking Confirmed!</h3>';
            if (smsContent) {
                html += '<p>' + smsContent + '</p>';
            } else {
                html += '<p>Your parking has been activated.</p>';
            }
            successEl.innerHTML = html;
            successEl.style.display = 'block';

            // Close modal after 5 seconds
            setTimeout(() => {
                modal.style.display = 'none';
                form.reset();
                hoursInput.value = 1;
                document.getElementById('paymentStatus').style.display = 'block';
                document.getElementById('paymentStatus').innerHTML =
                    '<div class="spinner"></div><p>Waiting for payment...</p>';
                document.getElementById('successMessage').style.display = 'none';
            }, 5000);
        }

        // Close modal when clicking outside
        window.addEventListener('click', (e) => {
            if (e.target === modal) {
                if (confirm('Are you sure you want to cancel this payment?')) {
                    clearInterval(checkPaymentInterval);
                    clearInterval(checkSMSInterval);
                    modal.style.display = 'none';
                }
            }
        });
    </script>
</body>
</html>`, zoneOptions)
}

func generateLegalPageCSS() string {
	return `
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: flex-start;
            padding: 40px 20px;
        }

        .container {
            background: white;
            border-radius: 20px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            padding: 40px;
            max-width: 700px;
            width: 100%;
        }

        h1 {
            color: #333;
            margin-bottom: 10px;
            font-size: 28px;
        }

        h2 {
            color: #333;
            margin-top: 25px;
            margin-bottom: 10px;
            font-size: 20px;
        }

        p, li {
            color: #555;
            line-height: 1.7;
            font-size: 15px;
            margin-bottom: 10px;
        }

        ul {
            margin-left: 20px;
            margin-bottom: 15px;
        }

        .back-link {
            display: inline-block;
            margin-bottom: 25px;
            color: #667eea;
            text-decoration: none;
            font-weight: 600;
            font-size: 14px;
        }

        .back-link:hover {
            text-decoration: underline;
        }

        .last-updated {
            color: #888;
            font-size: 13px;
            margin-bottom: 20px;
        }
`
}

func generatePrivacyHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Privacy Policy - Lightning Parking</title>
    <style>` + generateLegalPageCSS() + `</style>
</head>
<body>
    <div class="container">
        <a href="/" class="back-link">&larr; Back to Lightning Parking</a>
        <h1>Privacy Policy</h1>
        <p class="last-updated">Last updated: February 2025</p>

        <h2>1. Introduction</h2>
        <p>Lightning Parking ("we", "us", "our") operates a parking payment service that allows users to pay for parking using the Bitcoin Lightning Network. We are committed to protecting your personal data in accordance with the EU General Data Protection Regulation (GDPR) (Regulation (EU) 2016/679).</p>

        <h2>2. Data Controller</h2>
        <p>Lightning Parking is the data controller responsible for your personal data collected through this service.</p>

        <h2>3. Data We Collect</h2>
        <p>When you use our service, we collect and process the following personal data:</p>
        <ul>
            <li><strong>License plate number</strong> &ndash; required to register your parking session with the municipal parking system.</li>
            <li><strong>Parking zone</strong> &ndash; the selected zone where you are parking.</li>
            <li><strong>Timestamp</strong> &ndash; the date and time your parking session is initiated and its duration.</li>
            <li><strong>Payment data</strong> &ndash; Lightning Network invoice and payment hash (no personal financial information such as bank accounts or credit card numbers is collected).</li>
        </ul>

        <h2>4. Purpose and Legal Basis for Processing</h2>
        <p>We process your data for the following purposes:</p>
        <ul>
            <li><strong>To provide the parking payment service</strong> &ndash; your license plate, zone, and timestamp are transmitted to the parking authority to activate your parking session. The legal basis is performance of a contract (Article 6(1)(b) GDPR).</li>
            <li><strong>To process your payment</strong> &ndash; payment data is processed via the Lightning Network to complete your transaction. The legal basis is performance of a contract (Article 6(1)(b) GDPR).</li>
        </ul>

        <h2>5. Data Sharing</h2>
        <p>Your parking data (license plate, zone, and duration) is shared with the parking authority via SMS to register your parking session. We do not sell, trade, or otherwise share your personal data with third parties for marketing purposes.</p>

        <h2>6. Data Retention</h2>
        <p>We do not operate a persistent database. Parking session data is transmitted to the parking authority in real time and is not stored on our servers beyond the duration of your active session. Payment records may be retained by the Lightning Network payment provider (LNbits) according to their own retention policies.</p>

        <h2>7. Cookies</h2>
        <p>This website does not use cookies, tracking pixels, or any other tracking technologies. No data is stored on your device.</p>

        <h2>8. Your Rights Under GDPR</h2>
        <p>Under the GDPR, you have the following rights regarding your personal data:</p>
        <ul>
            <li><strong>Right of access</strong> &ndash; you may request a copy of the data we hold about you.</li>
            <li><strong>Right to rectification</strong> &ndash; you may request correction of inaccurate data.</li>
            <li><strong>Right to erasure</strong> &ndash; you may request deletion of your data where there is no compelling reason for its continued processing.</li>
            <li><strong>Right to restriction of processing</strong> &ndash; you may request that we limit how we use your data.</li>
            <li><strong>Right to data portability</strong> &ndash; you may request your data in a structured, machine-readable format.</li>
            <li><strong>Right to object</strong> &ndash; you may object to the processing of your data.</li>
        </ul>
        <p>To exercise any of these rights, please contact us using the details provided below.</p>

        <h2>9. Data Security</h2>
        <p>We use HTTPS encryption for all communications between your browser and our servers. Payment is processed via the Bitcoin Lightning Network, which does not require you to share any traditional financial information.</p>

        <h2>10. Contact</h2>
        <p>If you have any questions about this privacy policy or wish to exercise your data protection rights, please contact us at the address provided on the main website.</p>
    </div>
</body>
</html>`
}

func generateTermsHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Terms of Use - Lightning Parking</title>
    <style>` + generateLegalPageCSS() + `</style>
</head>
<body>
    <div class="container">
        <a href="/" class="back-link">&larr; Back to Lightning Parking</a>
        <h1>Terms of Use</h1>
        <p class="last-updated">Last updated: February 2025</p>

        <h2>1. Overview</h2>
        <p>Lightning Parking provides a service that allows you to pay for street parking using Bitcoin via the Lightning Network. By using this service, you agree to the following terms.</p>

        <h2>2. How the Service Works</h2>
        <p>The parking payment process works as follows:</p>
        <ul>
            <li><strong>Step 1:</strong> You enter your vehicle's license plate number, select the parking zone where your vehicle is parked, and choose the desired parking duration (in hours).</li>
            <li><strong>Step 2:</strong> The system calculates the parking fee in euros based on the zone rate and duration, then converts this amount to Bitcoin satoshis using real-time exchange rates.</li>
            <li><strong>Step 3:</strong> A Lightning Network invoice is generated. You can pay by scanning the QR code with a Lightning-compatible Bitcoin wallet or by copying the invoice manually.</li>
            <li><strong>Step 4:</strong> Once payment is confirmed on the Lightning Network, your parking session details (license plate, zone, and duration) are automatically sent to the parking authority to activate your parking.</li>
            <li><strong>Step 5:</strong> You will receive an on-screen confirmation once the parking authority has processed your session.</li>
        </ul>

        <h2>3. Consent to Data Processing</h2>
        <p>By using this service, you consent to the collection and processing of the following data:</p>
        <ul>
            <li>Your vehicle's <strong>license plate number</strong></li>
            <li>The <strong>parking zone</strong> you select</li>
            <li>The <strong>timestamp</strong> and <strong>duration</strong> of your parking session</li>
        </ul>
        <p>This data is necessary to register your parking session with the parking authority and is transmitted in real time. For full details on how your data is handled, please refer to our <a href="/privacy" style="color: #667eea;">Privacy Policy</a>.</p>

        <h2>4. Payment</h2>
        <p>All payments are made via the Bitcoin Lightning Network. Payments are final and non-reversible due to the nature of the Lightning Network. The amount in satoshis is calculated using the real-time BTC/EUR exchange rate at the time of invoice generation. Exchange rate fluctuations between invoice generation and payment are your responsibility.</p>

        <h2>5. Accuracy of Information</h2>
        <p>You are responsible for entering the correct license plate number, selecting the correct parking zone, and choosing the appropriate duration. Lightning Parking is not liable for parking fines or penalties resulting from incorrect information entered by the user.</p>

        <h2>6. Service Availability</h2>
        <p>We strive to keep the service available at all times, but we do not guarantee uninterrupted availability. The service depends on third-party systems including the Lightning Network payment infrastructure and the parking authority's SMS processing system. We are not liable for delays or failures caused by these external systems.</p>

        <h2>7. Limitation of Liability</h2>
        <p>Lightning Parking is provided "as is" without warranties of any kind. We are not liable for any direct, indirect, incidental, or consequential damages arising from your use of the service, including but not limited to parking fines resulting from system delays or failures.</p>

        <h2>8. Changes to These Terms</h2>
        <p>We reserve the right to update these terms at any time. Continued use of the service after changes are posted constitutes acceptance of the revised terms.</p>

        <h2>9. Contact</h2>
        <p>If you have any questions about these terms, please contact us at the address provided on the main website.</p>
    </div>
</body>
</html>`
}
