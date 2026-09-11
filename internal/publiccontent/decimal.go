package publiccontent

import (
	"fmt"
	"regexp"
	"strconv"
)

const (
	maxDecimalWhole = uint64(999999999999)
	decimalScale    = 8
)

var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,8})?$`)

// Decimal is a non-negative DECIMAL(20,8) value represented without floats.
// Its String form always has eight fractional digits for a stable wire value.
type Decimal struct {
	whole    uint64
	fraction uint32
}

// ParseDecimal accepts the public decimal syntax and rejects rounding,
// exponents, signs, whitespace, and values outside DECIMAL(20,8).
func ParseDecimal(raw string) (Decimal, error) {
	if !decimalPattern.MatchString(raw) {
		return Decimal{}, fmt.Errorf("invalid decimal")
	}

	wholeText := raw
	fractionText := ""
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			wholeText, fractionText = raw[:i], raw[i+1:]
			break
		}
	}
	whole, err := strconv.ParseUint(wholeText, 10, 64)
	if err != nil || whole > maxDecimalWhole {
		return Decimal{}, fmt.Errorf("decimal out of range")
	}
	for len(fractionText) < decimalScale {
		fractionText += "0"
	}
	fraction, err := strconv.ParseUint(fractionText, 10, 32)
	if err != nil {
		return Decimal{}, fmt.Errorf("invalid decimal")
	}
	return Decimal{whole: whole, fraction: uint32(fraction)}, nil
}

func (value Decimal) String() string {
	return fmt.Sprintf("%d.%08d", value.whole, value.fraction)
}
