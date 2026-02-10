package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"ljightningparking/lnd"
	"ljightningparking/parking"
	"log"
	"net/http"
	"strconv"
)

var BaseTemplate *template.Template

func MainHandler(w http.ResponseWriter, r *http.Request) {

	if len(r.RequestURI) > 1 || r.Method != "GET" {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	err := BaseTemplate.ExecuteTemplate(w, "main", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("template execution failed")
	}
}

func PayHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "POST" {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	zoneName := r.FormValue("zone")

	payZone, ok := parking.Zones[zoneName]

	if !ok {
		http.Error(w, fmt.Sprintf("zone does not exit: %s", zoneName), http.StatusInternalServerError)
		return
	}

	plate := r.FormValue("plate")

	if checkPlate(plate) != nil {
		http.Error(w, fmt.Sprintf("invalid licence plate: %s", plate), http.StatusInternalServerError)
		return
	}

	hours := r.FormValue("hours")

	hoursInt, err := strconv.ParseInt(hours, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid hours to park: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	//invoice := lnd.InvoiceHandler.GetInvoice(payZone, plate, hoursInt)
	//if len(invoice.PaymentRequest) == 0 {
	//	http.Error(w, fmt.Sprintf("error while generating ln invoice"), http.StatusInternalServerError)
	//	return
	//}

	key := lnd.InvoiceKey{
		Zone:  payZone,
		Plate: plate,
		Hours: hoursInt,
	}

	data := struct {
		PaymentRequest string
		SmsData string
	}{
		"someLnPaymentRequest",
		key.Message(),
	}

	err = BaseTemplate.ExecuteTemplate(w, "pay", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("template execution failed: %s", err)
	}

}

func CheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	data, ok := r.URL.Query()["paymentRequest"]
	if !ok || len(data[0]) < 1 {
		http.Error(w, "paymentRequest parameter missing", http.StatusNotFound)
		log.Print("error parsing url: missing data parameter")
		return
	}

	response := make(map[string]interface{})
	response["paymentRequest"] = data[0]
	response["isPaid"] = lnd.InvoiceHandler.CheckInvoice(data[0])

	err := json.NewEncoder(w).Encode(response)
	if err != nil {
		http.Error(w, "error encoding json response", http.StatusNotFound)
		log.Printf("error encoding check invoice response: %s", err)
	}

}

func checkPlate(plate string) error {
	return nil
}