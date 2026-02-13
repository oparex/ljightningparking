package main

import (
	"bytes"
	"encoding/json"
	"flag"
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
	LNBitsURL      string
	LNBitsAPIKey   string
	SMSServer      string
	CallbackAPIKey string
	SMSNumber      string
}

var config Config

// SubmitRequest represents the incoming form data
type SubmitRequest struct {
	Plate string `json:"plate"`
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
	port := flag.Int("port", 9090, "server port")
	flag.Parse()

	// Load configuration from environment variables
	config = Config{
		LNBitsURL:      getEnv("LNBITS_URL", "https://legend.lnbits.com"),
		LNBitsAPIKey:   getEnv("LNBITS_API_KEY", ""),
		SMSServer:      getEnv("SMS_SERVER", ""),
		CallbackAPIKey: getEnv("CALLBACK_API_KEY", ""),
		SMSNumber:      getEnv("SMS_NUMBER", ""),
	}

	if config.LNBitsAPIKey == "" {
		log.Fatal("LNBITS_API_KEY environment variable is required")
	}

	if config.SMSServer == "" {
		log.Fatal("SMS_SERVER environment variable is required")
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

	log.Printf("Starting server on port %d", *port)
	router.Run(fmt.Sprintf(":%d", *port))
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

	// Validate plate (skip for Donate zone)
	if req.Zone != "Donate" && len(req.Plate) < 2 {
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
	var memo string
	if req.Zone == "Donate" {
		memo = fmt.Sprintf("Donate (%.2f EUR)", feeEuros)
	} else {
		memo = fmt.Sprintf("Parking: %s @ %s for %d hours (%.2f EUR)", req.Plate, req.Zone, req.Hours, feeEuros)
	}
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

		// Skip SMS for donate zone
		if zone == "Donate" {
			log.Printf("Donation received from plate %s", plate)
			c.JSON(http.StatusOK, gin.H{"paid": true, "donate": true})
			return
		}

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
	req, err := http.NewRequest("GET", config.SMSServer+"/wakeup", nil)
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
	searchURL := fmt.Sprintf("%s/received/search?q=%s&after=%s", config.SMSServer, url.QueryEscape(plate), url.QueryEscape(after))

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

	req, err := http.NewRequest("POST", config.SMSServer+"/send", bytes.NewBuffer(jsonData))
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
	// Add Donate option first with a special label
	donateInfo := parking.Zones["Donate"]
	zoneOptions := fmt.Sprintf(`<option value="Donate">Not in Ljubljana? Donate! - €%.2f</option>`,
		donateInfo.Price)
	for _, zone := range zones {
		if zone == "Donate" {
			continue
		}
		zoneInfo := parking.Zones[zone]
		// Build option - no escaping needed since it's passed as argument, not part of template
		zoneOptions += fmt.Sprintf(`<option value="%s">%s - €%.2f/hr (max %dh)</option>`,
			zone, zone, zoneInfo.Price, int(zoneInfo.MaxTime))
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
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
            background: linear-gradient(135deg, #1e3c72 0%%, #2a5298 50%%, #7e22ce 100%%);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            padding: 20px;
            position: relative;
        }

        body::before {
            content: '';
            position: absolute;
            width: 400px;
            height: 400px;
            background: radial-gradient(circle, rgba(255, 255, 255, 0.1) 0%%, transparent 70%%);
            border-radius: 50%%;
            top: -100px;
            right: -100px;
            animation: float 20s infinite ease-in-out;
        }

        body::after {
            content: '';
            position: absolute;
            width: 300px;
            height: 300px;
            background: radial-gradient(circle, rgba(255, 255, 255, 0.08) 0%%, transparent 70%%);
            border-radius: 50%%;
            bottom: -80px;
            left: -80px;
            animation: float 15s infinite ease-in-out reverse;
        }

        @keyframes float {
            0%%, 100%% { transform: translate(0, 0) scale(1); }
            50%% { transform: translate(30px, 30px) scale(1.1); }
        }

        .container {
            background: rgba(255, 255, 255, 0.95);
            backdrop-filter: blur(10px);
            border-radius: 24px;
            box-shadow: 0 24px 48px rgba(0, 0, 0, 0.3), 0 0 0 1px rgba(255, 255, 255, 0.1);
            padding: 48px;
            max-width: 480px;
            width: 100%%;
            position: relative;
            z-index: 1;
            animation: slideUp 0.6s ease-out;
        }

        @keyframes slideUp {
            from {
                opacity: 0;
                transform: translateY(30px);
            }
            to {
                opacity: 1;
                transform: translateY(0);
            }
        }

        h1 {
            color: #1a202c;
            margin-bottom: 8px;
            font-size: 32px;
            text-align: center;
            font-weight: 700;
            letter-spacing: -0.5px;
        }

        .subtitle {
            color: #64748b;
            text-align: center;
            margin-bottom: 36px;
            font-size: 15px;
            font-weight: 500;
        }

        .form-group {
            margin-bottom: 24px;
        }

        label {
            display: block;
            margin-bottom: 10px;
            color: #334155;
            font-weight: 600;
            font-size: 14px;
            letter-spacing: 0.2px;
            text-transform: uppercase;
        }

        /* Unified input style for all three inputs */
        input[type="text"],
        select,
        .hours-selector {
            width: 100%%;
            padding: 16px 20px;
            border: 2px solid #e2e8f0;
            border-radius: 16px;
            font-size: 16px;
            font-weight: 500;
            color: #1a202c;
            background: white;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
            appearance: none;
            box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
        }

        input[type="text"]:focus,
        select:focus,
        .hours-selector:focus-within {
            outline: none;
            border-color: #7e22ce;
            box-shadow: 0 0 0 3px rgba(126, 34, 206, 0.1), 0 4px 12px rgba(0, 0, 0, 0.1);
            transform: translateY(-2px);
        }

        input[type="text"]:hover,
        select:hover,
        .hours-selector:hover {
            border-color: #cbd5e1;
        }

        /* Custom dropdown arrow for select - SVG arrow icon */
        select {
            background-image: url("data:image/svg+xml,%%3Csvg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='%%231a202c' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%%3E%%3Cpolyline points='6 9 12 15 18 9'%%3E%%3C/polyline%%3E%%3C/svg%%3E");
            background-repeat: no-repeat;
            background-position: right 16px center;
            background-size: 20px;
            padding-right: 48px;
            cursor: pointer;
        }

        /* Hours selector with the same style as other inputs */
        .hours-selector {
            display: flex;
            align-items: center;
            justify-content: space-between;
            cursor: default;
            padding: 12px 16px;
        }

        .hours-selector button {
            width: 40px;
            height: 40px;
            border: none;
            background: linear-gradient(135deg, #7e22ce 0%%, #a855f7 100%%);
            color: white;
            border-radius: 12px;
            font-size: 20px;
            font-weight: bold;
            cursor: pointer;
            transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
            display: flex;
            align-items: center;
            justify-content: center;
            box-shadow: 0 2px 8px rgba(126, 34, 206, 0.3);
        }

        .hours-selector button:hover:not(:disabled) {
            background: linear-gradient(135deg, #6b21a8 0%%, #9333ea 100%%);
            transform: scale(1.05);
            box-shadow: 0 4px 12px rgba(126, 34, 206, 0.4);
        }

        .hours-selector button:active:not(:disabled) {
            transform: scale(0.95);
        }

        .hours-selector button:disabled {
            opacity: 0.4;
            cursor: not-allowed;
            background: #94a3b8;
            box-shadow: none;
        }

        .hours-selector .hours-display {
            flex: 1;
            text-align: center;
            font-size: 20px;
            font-weight: 700;
            color: #1a202c;
            letter-spacing: -0.5px;
        }

        .hours-selector input[type="number"] {
            display: none;
        }

        .submit-btn {
            width: 100%%;
            padding: 18px;
            background: linear-gradient(135deg, #7e22ce 0%%, #a855f7 50%%, #ec4899 100%%);
            color: white;
            border: none;
            border-radius: 16px;
            font-size: 17px;
            font-weight: 700;
            cursor: pointer;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
            box-shadow: 0 8px 24px rgba(126, 34, 206, 0.35);
            letter-spacing: 0.3px;
            text-transform: uppercase;
            margin-top: 12px;
        }

        .submit-btn:hover {
            transform: translateY(-3px);
            box-shadow: 0 12px 32px rgba(126, 34, 206, 0.5);
            background: linear-gradient(135deg, #6b21a8 0%%, #9333ea 50%%, #db2777 100%%);
        }

        .submit-btn:active {
            transform: translateY(-1px);
            box-shadow: 0 6px 20px rgba(126, 34, 206, 0.4);
        }

        .submit-btn:disabled {
            opacity: 0.6;
            cursor: not-allowed;
            transform: none;
            box-shadow: 0 4px 12px rgba(126, 34, 206, 0.2);
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
            background-color: rgba(0, 0, 0, 0.75);
            backdrop-filter: blur(8px);
            animation: fadeIn 0.4s cubic-bezier(0.4, 0, 0.2, 1);
            overflow-y: auto;
            -webkit-overflow-scrolling: touch;
        }

        @keyframes fadeIn {
            from { opacity: 0; }
            to { opacity: 1; }
        }

        .modal-content {
            background: linear-gradient(135deg, rgba(255, 255, 255, 0.98) 0%%, rgba(255, 255, 255, 0.95) 100%%);
            backdrop-filter: blur(10px);
            margin: 5%% auto;
            padding: 36px;
            border-radius: 24px;
            max-width: 520px;
            width: 90%%;
            box-shadow: 0 32px 64px rgba(0, 0, 0, 0.3), 0 0 0 1px rgba(255, 255, 255, 0.1);
            animation: slideIn 0.4s cubic-bezier(0.4, 0, 0.2, 1);
            position: relative;
        }

        .modal-close {
            position: absolute;
            top: 12px;
            right: 16px;
            background: none;
            border: none;
            font-size: 28px;
            color: #94a3b8;
            cursor: pointer;
            width: 36px;
            height: 36px;
            display: flex;
            align-items: center;
            justify-content: center;
            border-radius: 50%%;
            transition: all 0.2s;
        }

        .modal-close:hover {
            background: #f1f5f9;
            color: #1a202c;
        }

        @keyframes slideIn {
            from {
                transform: translateY(-60px) scale(0.95);
                opacity: 0;
            }
            to {
                transform: translateY(0) scale(1);
                opacity: 1;
            }
        }

        .modal h2 {
            color: #1a202c;
            margin-bottom: 24px;
            text-align: center;
            font-size: 28px;
            font-weight: 700;
            letter-spacing: -0.5px;
        }

        .invoice-container {
            background: linear-gradient(135deg, #f8fafc 0%%, #f1f5f9 100%%);
            padding: 20px;
            border-radius: 16px;
            margin: 20px 0;
            word-break: break-all;
            border: 2px solid #e2e8f0;
        }

        .invoice-details {
            margin-bottom: 20px;
            padding: 20px;
            background: linear-gradient(135deg, #ffffff 0%%, #fefefe 100%%);
            border-radius: 16px;
            border: 2px solid #e2e8f0;
            box-shadow: 0 2px 8px rgba(0, 0, 0, 0.05);
        }

        .invoice-details p {
            margin: 10px 0;
            color: #64748b;
            font-size: 15px;
        }

        .invoice-details strong {
            color: #1a202c;
            font-weight: 600;
        }

        .qr-code {
            text-align: center;
            margin: 24px 0;
            padding: 20px;
            background: white;
            border-radius: 16px;
            border: 2px solid #e2e8f0;
        }

        .qr-code img {
            max-width: 256px;
            width: 100%%;
            border-radius: 12px;
        }

        .copy-btn {
            width: 100%%;
            padding: 14px;
            background: linear-gradient(135deg, #7e22ce 0%%, #a855f7 100%%);
            color: white;
            border: none;
            border-radius: 12px;
            font-size: 15px;
            font-weight: 600;
            cursor: pointer;
            margin-top: 12px;
            transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
            box-shadow: 0 4px 12px rgba(126, 34, 206, 0.3);
        }

        .copy-btn:hover {
            background: linear-gradient(135deg, #6b21a8 0%%, #9333ea 100%%);
            transform: translateY(-2px);
            box-shadow: 0 6px 16px rgba(126, 34, 206, 0.4);
        }

        .copy-btn:active {
            transform: translateY(0);
        }

        .loading {
            text-align: center;
            color: #7e22ce;
            font-weight: 600;
            margin: 20px 0;
            font-size: 15px;
        }

        .spinner {
            border: 3px solid #e2e8f0;
            border-top: 3px solid #7e22ce;
            border-radius: 50%%;
            width: 48px;
            height: 48px;
            animation: spin 1s linear infinite;
            margin: 24px auto;
        }

        @keyframes spin {
            0%% { transform: rotate(0deg); }
            100%% { transform: rotate(360deg); }
        }

        .success-message {
            background: linear-gradient(135deg, #10b981 0%%, #059669 100%%);
            color: white;
            padding: 24px;
            border-radius: 16px;
            text-align: center;
            margin: 20px 0;
            box-shadow: 0 8px 24px rgba(16, 185, 129, 0.3);
        }

        .success-message h3 {
            font-size: 22px;
            margin-bottom: 8px;
        }

        .success-message p {
            font-size: 15px;
            opacity: 0.95;
        }

        .error {
            color: #ef4444;
            font-size: 14px;
            margin-top: 12px;
            text-align: center;
            font-weight: 500;
        }

        .footer {
            margin-top: 32px;
            padding-top: 24px;
            border-top: 2px solid #e2e8f0;
            text-align: center;
            font-size: 13px;
            color: #94a3b8;
        }

        .footer a {
            color: #7e22ce;
            text-decoration: none;
            margin: 0 12px;
            font-weight: 500;
            transition: color 0.2s;
        }

        .footer a:hover {
            color: #6b21a8;
            text-decoration: underline;
        }

        /* Responsive design for mobile */
        @media (max-width: 768px) {
            body {
                align-items: flex-start;
                padding: 32px 12px;
            }

            .container {
                padding: 28px 22px;
                border-radius: 20px;
            }

            h1 {
                font-size: 26px;
                margin-bottom: 6px;
            }

            .subtitle {
                font-size: 14px;
                margin-bottom: 28px;
            }

            .form-group {
                margin-bottom: 20px;
            }

            label {
                margin-bottom: 8px;
                font-size: 13px;
            }

            input[type="text"],
            select,
            .hours-selector {
                padding: 13px 16px;
                font-size: 15px;
                border-radius: 14px;
            }

            .hours-selector {
                padding: 10px 14px;
            }

            .hours-selector button {
                width: 36px;
                height: 36px;
                font-size: 18px;
            }

            .hours-selector .hours-display {
                font-size: 18px;
            }

            .submit-btn {
                padding: 15px;
                font-size: 16px;
                margin-top: 10px;
            }

            .footer {
                margin-top: 24px;
                padding-top: 18px;
            }

            .modal-content {
                padding: 28px 20px;
                margin: 10%% auto;
            }

            .modal h2 {
                font-size: 24px;
            }

            .footer {
                font-size: 12px;
            }

            body::before,
            body::after {
                display: none;
            }
        }
    </style>
</head>
<body>
    <div class="container">
		<h1>⚡⚡⚡</h1>
        <h1>Lightning Parking</h1>
        <p class="subtitle">Pay for your parking in Ljubljana with Bitcoin</p>

        <form id="parkingForm" novalidate>
            <div class="form-group">
                <label for="plate">License Plate</label>
                <input type="text" id="plate" name="plate" placeholder="Enter your license plate">
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
                <div class="hours-selector">
                    <button type="button" id="decreaseHours">−</button>
                    <div class="hours-display" id="hoursDisplay">1</div>
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
            <button class="modal-close" id="modalClose" title="Close">&times;</button>
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
        const hoursDisplay = document.getElementById('hoursDisplay');
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

        // Wake up SMS server once all fields are filled (skip for Donate zone)
        function triggerWakeup() {
            if (wakeupSent) return;
            const plate = document.getElementById('plate').value.trim();
            const zone = document.getElementById('zone').value;
            if (!plate || !zone || zone === 'Donate') return;
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
                hoursDisplay.textContent = current - 1;
            }
        });

        increaseBtn.addEventListener('click', () => {
            const current = parseInt(hoursInput.value);
            if (current < 24) {
                hoursInput.value = current + 1;
                hoursDisplay.textContent = current + 1;
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

            if (formData.zone !== 'Donate' && formData.plate.length < 2) {
                formError.textContent = 'Please enter a valid license plate';
                submitBtn.disabled = false;
                submitBtn.textContent = 'Generate Invoice';
                return;
            }

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
                        if (data.donate) {
                            showSuccess('Thank you for your donation!');
                        } else {
                            document.getElementById('paymentStatus').innerHTML =
                                '<div class="spinner"></div><p>Payment received! Confirming parking...</p>';
                            startSMSCheck(currentParkingData.plate, data.sms_sent_at);
                        }
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
                hoursDisplay.textContent = 1;
                document.getElementById('paymentStatus').style.display = 'block';
                document.getElementById('paymentStatus').innerHTML =
                    '<div class="spinner"></div><p>Waiting for payment...</p>';
                document.getElementById('successMessage').style.display = 'none';
            }, 5000);
        }

        function closeModal() {
            if (confirm('Are you sure you want to cancel this payment?')) {
                clearInterval(checkPaymentInterval);
                clearInterval(checkSMSInterval);
                modal.style.display = 'none';
            }
        }

        // Close modal with X button
        document.getElementById('modalClose').addEventListener('click', closeModal);

        // Close modal when clicking outside
        window.addEventListener('click', (e) => {
            if (e.target === modal) {
                closeModal();
            }
        });
    </script>
</body>
</html>`, zoneOptions)

	return html
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
