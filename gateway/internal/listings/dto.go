package listings

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

type listingRequest struct {
	Title *string      `json:"title"`
	Price *priceNumber `json:"price"`
	Stock *int64       `json:"stock"`
}

type priceNumber string

func (p *priceNumber) UnmarshalJSON(data []byte) error {
	if len(data) > 64 || len(data) == 0 || data[0] == '"' {
		return fmt.Errorf("price must be a JSON number of at most 64 characters")
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	if index := strings.IndexAny(string(number), "eE"); index >= 0 {
		exponent, err := strconv.Atoi(string(number)[index+1:])
		if err != nil || exponent < -64 || exponent > 64 {
			return fmt.Errorf("price exponent is out of range")
		}
	}
	*p = priceNumber(number)
	return nil
}

func (r listingRequest) validate(create bool) (*int64, []fieldError) {
	var fields []fieldError
	if r.Title == nil && create {
		fields = append(fields, fieldError{Field: "title", Message: "is required"})
	} else if r.Title != nil && (strings.TrimSpace(*r.Title) == "" || utf8.RuneCountInString(*r.Title) > 200) {
		fields = append(fields, fieldError{Field: "title", Message: "must be non-empty and at most 200 characters"})
	}
	var price *int64
	if r.Price == nil && create {
		fields = append(fields, fieldError{Field: "price", Message: "is required"})
	} else if r.Price != nil {
		value, ok := new(big.Rat).SetString(string(*r.Price))
		if ok {
			value.Mul(value, big.NewRat(100, 1))
		}
		if !ok || value.Sign() <= 0 || !value.IsInt() || !value.Num().IsInt64() {
			fields = append(fields, fieldError{Field: "price", Message: "must be positive, fit int64 minor units and have at most two decimal places"})
		} else {
			minor := value.Num().Int64()
			price = &minor
		}
	}
	if r.Stock == nil && create {
		fields = append(fields, fieldError{Field: "stock", Message: "is required"})
	} else if r.Stock != nil && *r.Stock < 0 {
		fields = append(fields, fieldError{Field: "stock", Message: "must be non-negative"})
	}
	return price, fields
}
