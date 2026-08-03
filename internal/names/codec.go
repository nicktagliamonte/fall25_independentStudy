package names

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var (
	canonicalMode cbor.EncMode
	strictMode    cbor.DecMode
)

func init() {
	var err error
	canonicalMode, err = cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	strictMode, err = (cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		TagsMd:            cbor.TagsForbidden,
		MaxNestedLevels:   16,
		MaxArrayElements:  1 << 20,
		MaxMapPairs:       1 << 16,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
	}).DecMode()
	if err != nil {
		panic(err)
	}
}

func MarshalCanonical(value any) ([]byte, error) {
	return canonicalMode.Marshal(value)
}

func UnmarshalCanonical(data []byte, value any) error {
	if len(data) == 0 {
		return fmt.Errorf("empty DAG-CBOR value")
	}
	if err := strictMode.Unmarshal(data, value); err != nil {
		return err
	}
	reencoded, err := canonicalMode.Marshal(value)
	if err != nil {
		return err
	}
	if string(reencoded) != string(data) {
		return fmt.Errorf("record is not canonical DAG-CBOR")
	}
	return nil
}
