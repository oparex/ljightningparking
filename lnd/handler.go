package lnd

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"ljightningparking/parking"
	"ljightningparking/price"
	"ljightningparking/sms"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const SETTLED = "SETTLED"

type Handler struct {
	httpClient http.Client
	macaroon   string
	invoices   InvoiceCache
	lndAddress string
}

type InvoiceCache struct {
	keyToInvoice map[InvoiceKey]Invoice
	invoiceToKey map[string]InvoiceKey
	sync.Mutex
}

type InvoiceKey struct {
	Zone  parking.Zone
	Plate string
	Hours int64
}

func (k InvoiceKey) Message() string {
	return fmt.Sprintf("%s %s %d", k.Zone.Name, k.Plate, k.Hours)
}

func (k InvoiceKey) GetSatsToPay() int64 {

	btcPrice := price.GetPrice("btceur")
	if btcPrice < 0 {
		return -1
	}

	return int64(k.Zone.GetParkingFee(k.Hours) / btcPrice * 1e-8)
}

type Invoice struct {
	PaymentRequest string
	Expiry         int64
}

type RpcResponse struct {
	Error  interface{} `json:"error"`
	Result RpcInvoice  `json:"result"`
}

type RpcInvoice struct {
	PaymentRequest string `json:"payment_request"`
	CreationDate   int64  `json:"creation_date"`
	Expiry         int64  `json:"Expiry"`
	State          string `json:"state"`
}

var InvoiceHandler *Handler

func InitHandler(lndAddress, macaroonPath string) {

	f, err := os.Open(macaroonPath)
	if err != nil {
		log.Fatalf("Error loading macaroon file: %v", err)
	}
	defer f.Close()

	data, err := ioutil.ReadAll(f)

	insecureTransport := http.DefaultTransport
	insecureTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	InvoiceHandler = &Handler{
		httpClient: http.Client{
			Transport: insecureTransport,
			Timeout:   5 * time.Second,
		},
		macaroon: fmt.Sprintf("%02x", data),
		invoices: InvoiceCache{
			keyToInvoice: make(map[InvoiceKey]Invoice),
			invoiceToKey: make(map[string]InvoiceKey),
			Mutex:        sync.Mutex{},
		},
		lndAddress: lndAddress,
	}

	go InvoiceHandler.RunInvoiceChecker()
}

func (h *Handler) GetInvoice(zone parking.Zone, plate string, hours int64) Invoice {

	key := InvoiceKey{zone, plate, hours}

	h.invoices.Lock()
	inv, ok := h.invoices.keyToInvoice[key]
	h.invoices.Unlock()

	now := time.Now().Unix()

	if ok && inv.Expiry > now {
		return inv
	}

	satsToPay := key.GetSatsToPay()
	if satsToPay < 0 {
		log.Printf("Error while getting sats to pay")
		return Invoice{}
	}

	request, err := http.NewRequest("POST", fmt.Sprintf("https://%s/v1/invoices", h.lndAddress), strings.NewReader(fmt.Sprintf(`{"expiry": 300, "value": %d}`, satsToPay)))
	if err != nil {
		log.Fatalf("Error constructing a new request struct: %v", err)
	}

	request.Header.Set("Grpc-Metadata-macaroon", h.macaroon)

	resp, err := h.httpClient.Do(request)
	if err != nil {
		log.Fatalf("Error making a post request to get a new inv: %v", err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading response body: %v", err)
	}

	var response RpcInvoice
	err = json.Unmarshal(body, &response)
	if err != nil {
		log.Fatalf("Error unmarshling new inv: %v", err)
	}

	newInvoice := Invoice{
		PaymentRequest: response.PaymentRequest,
		Expiry:         now + 300,
	}

	h.invoices.Lock()
	h.invoices.keyToInvoice[key] = newInvoice
	h.invoices.invoiceToKey[response.PaymentRequest] = key
	h.invoices.Unlock()

	go func(paymentRequest string) {
		time.Sleep(300*time.Second)
		h.invoices.Lock()
		id, ok := h.invoices.invoiceToKey[paymentRequest]
		if ok {
			delete(h.invoices.invoiceToKey, paymentRequest)
			delete(h.invoices.keyToInvoice, id)
		}
		h.invoices.Unlock()
	}(response.PaymentRequest)

	return newInvoice
}

func (h *Handler) RunInvoiceChecker() {
	conn, err := tls.Dial("tcp", h.lndAddress, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		log.Fatalf("Error connecting to lnd rpc server: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte(fmt.Sprintf("GET /v1/invoices/subscribe HTTP/1.0\nGrpc-Metadata-macaroon: %s\r\n\r\n", h.macaroon)))
	if err != nil {
		log.Fatalf("Error writing to lnd rpc server: %v", err)
	}

	reader := bufio.NewReader(conn)

	for {
		msg, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			log.Fatalf("Error reading from lnd rpc server: %v", readErr)
		}

		var response RpcResponse
		err = json.Unmarshal(msg, &response)
		if err == nil {
			if response.Error != nil {
				log.Printf("Error from rpc server: %v", response.Error)
				continue
			}
			log.Println(response)
			if response.Result.State != SETTLED {
				continue
			}
			h.invoices.Lock()
			key, ok := h.invoices.invoiceToKey[response.Result.PaymentRequest]
			if ok {
				smsErr := sms.Send(key.Message())
				if smsErr != nil {
					log.Printf("Error sending sms: %s", err)
				}
				delete(h.invoices.invoiceToKey, response.Result.PaymentRequest)
				delete(h.invoices.keyToInvoice, key)
			}
			h.invoices.Unlock()
		}

		if readErr == io.EOF {
			break
		}
	}
}

func (h *Handler) CheckInvoice(paymentRequest string) bool {
	h.invoices.Lock()
	defer h.invoices.Unlock()

	_, ok := h.invoices.invoiceToKey[paymentRequest]

	return !ok
}
