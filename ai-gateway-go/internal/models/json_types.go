package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

type JSONSlice []string

func (j JSONSlice) Value() (driver.Value, error) {
	if j == nil {
		return "[]", nil
	}
	b, err := json.Marshal(j)
	return string(b), err
}

func (j *JSONSlice) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("invalid JSONSlice scan type")
	}
	return json.Unmarshal(bytes, j)
}

type JSONIntSlice []int

func (j JSONIntSlice) Value() (driver.Value, error) {
	if j == nil {
		return "[]", nil
	}
	b, err := json.Marshal(j)
	return string(b), err
}

func (j *JSONIntSlice) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("invalid JSONIntSlice scan type")
	}
	return json.Unmarshal(bytes, j)
}

type JSONMap map[string]interface{}

func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	b, err := json.Marshal(j)
	return string(b), err
}

func (j *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	}
	if len(bytes) == 0 {
		if s, ok := value.(string); ok {
			bytes = []byte(s)
		}
	}
	return json.Unmarshal(bytes, j)
}
