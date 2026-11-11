package gmaps

import (
	"strconv"
	"strings"
)

// cidFromDataID returns the decimal form of the hex value after ":" in a
// data_id such as "0x14a1a32f316b15a1:0x169c54b46dcc3a93". It returns "" when
// the data_id is empty or malformed, or when the value does not fit in uint64.
func cidFromDataID(dataID string) string {
	_, hexPart, found := strings.Cut(dataID, ":")
	if !found {
		return ""
	}

	hexPart = strings.TrimPrefix(strings.TrimPrefix(hexPart, "0x"), "0X")
	if hexPart == "" {
		return ""
	}

	v, err := strconv.ParseUint(hexPart, 16, 64)
	if err != nil {
		return ""
	}

	return strconv.FormatUint(v, 10)
}
