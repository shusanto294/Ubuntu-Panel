package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// countryService places addresses in countries, a hundred at a time. Its free
// tier is plain HTTP and allows fifteen batches a minute, which is plenty for
// a page that asks once per address and keeps the answer.
const countryService = "http://ip-api.com/batch?fields=status,countryCode,query"

// countryBatch is the most addresses the service takes in one request.
const countryBatch = 100

// Placeable is whether an address could belong to a country at all: one on a
// private, loopback or link-local range is somewhere on this side of the
// internet, and asking about it would only send it somewhere for nothing.
func Placeable(address string) bool {
	ip := net.ParseIP(address)

	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsUnspecified() && !ip.IsMulticast()
}

// LookupCountries asks the service where each address is, as two-letter
// codes. Every address asked about is in the result — "" for one it could not
// place — so the caller can remember the misses as well as the hits.
func LookupCountries(addresses []string) (map[string]string, error) {
	if len(addresses) > countryBatch {
		addresses = addresses[:countryBatch]
	}

	body, _ := json.Marshal(addresses)

	response, err := (&http.Client{Timeout: 8 * time.Second}).Post(countryService,
		"application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the country lookup answered %s", response.Status)
	}

	var answers []struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		Query       string `json:"query"`
	}

	if err := json.NewDecoder(response.Body).Decode(&answers); err != nil {
		return nil, err
	}

	countries := make(map[string]string, len(addresses))
	for _, address := range addresses {
		countries[address] = ""
	}

	for _, answer := range answers {
		if _, asked := countries[answer.Query]; asked && answer.Status == "success" &&
			len(answer.CountryCode) == 2 {
			countries[answer.Query] = strings.ToUpper(answer.CountryCode)
		}
	}

	return countries, nil
}
