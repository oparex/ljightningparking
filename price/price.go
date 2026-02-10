package price

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

func GetPrice(pair string) float64 {

	resp, err := http.Get(fmt.Sprintf("https://www.bitstamp.net/api/v2/ticker/%s/", pair))
	if err != nil {
		log.Printf("Error while getting %s price: %s", pair, err)
		return -1
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error while reading response for %s price: %s", pair, err)
		return -1
	}

	var tickerJson struct{
		Last float64 `json:"last,string"`
	}

	err = json.Unmarshal(body, &tickerJson)
	if err != nil {
		log.Printf("Error while unmarshaling ticker response for %s price: %s", pair, err)
		return -1
	}

	return tickerJson.Last

}

// EuroToSatoshis converts an amount in euros to satoshis using current BTC/EUR price
func EuroToSatoshis(euros float64) int64 {
	btcPrice := GetPrice("btceur")
	if btcPrice <= 0 {
		log.Printf("Failed to get BTC price")
		return -1
	}

	// Convert euros to BTC, then to satoshis (1 BTC = 100,000,000 satoshis)
	btcAmount := euros / btcPrice
	satoshis := int64(btcAmount * 100000000)

	return satoshis
}
