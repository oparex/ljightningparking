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
