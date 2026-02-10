package price

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
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

	body, err := ioutil.ReadAll(resp.Body)
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
