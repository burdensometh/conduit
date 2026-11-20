package appevents

import (
	"crypto/sha512"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/jackc/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/algorand/go-algorand-sdk/v2/abi"
	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// selectorVector computes the ARC-28 marker independently of the
// implementation under test.
func selectorVector(t *testing.T, signature string) [selectorLength]byte {
	t.Helper()
	var selector [selectorLength]byte
	hash := sha512.Sum512_256([]byte(signature))
	copy(selector[:], hash[:selectorLength])
	return selector
}

func TestCompileEventSignature(t *testing.T) {
	def, err := compileEvent(&EventConfig{
		Name:      "transfer",
		Signature: "Transfer(address,address,uint64)",
		Args: []EventArgConfig{
			{Name: "sender"},
			{Name: "receiver"},
			{Name: "amount"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "transfer", def.name)
	assert.Equal(t, "app_event_transfer", def.table)
	assert.Equal(t, "Transfer(address,address,uint64)", def.signature)
	// Independently computed vector: sha512/256("Transfer(address,address,uint64)")[0:4]
	assert.Equal(t, "b1e134ac", hex.EncodeToString(def.selector[:]))
	assert.Equal(t, selectorVector(t, "Transfer(address,address,uint64)"), def.selector)
	require.Len(t, def.args, 3)
	assert.Equal(t, "arg_sender", def.args[0].colName)
	assert.Equal(t, "TEXT", def.args[0].sqlType)
	assert.Equal(t, "arg_receiver", def.args[1].colName)
	assert.Equal(t, "arg_amount", def.args[2].colName)
	assert.Equal(t, "NUMERIC(20,0)", def.args[2].sqlType)
}

func TestCompileEventSignatureCanonicalization(t *testing.T) {
	// Spacing and type aliases are canonicalized before hashing.
	def, err := compileEvent(&EventConfig{
		Name:      "e",
		Signature: "Transfer( address,address,uint64 )",
	})
	require.NoError(t, err)
	assert.Equal(t, "Transfer(address,address,uint64)", def.signature)
	assert.Equal(t, selectorVector(t, "Transfer(address,address,uint64)"), def.selector)
}

func TestCompileEventSelectorOnly(t *testing.T) {
	def, err := compileEvent(&EventConfig{
		Name:     "swap",
		Selector: "0x979289a2",
		Args: []EventArgConfig{
			{Name: "amount_in", Type: "uint64"},
			{Name: "amount_out", Type: "uint64"},
			{Name: "pool", Type: "address"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "app_event_swap", def.table)
	assert.Equal(t, "979289a2", hex.EncodeToString(def.selector[:]))
	assert.Equal(t, selectorVector(t, "Swap(uint64,uint64,address)"), def.selector)
	require.Len(t, def.args, 3)
	assert.Equal(t, "arg_amount_in", def.args[0].colName)
	assert.Equal(t, "arg_pool", def.args[2].colName)
	assert.Equal(t, "TEXT", def.args[2].sqlType)
}

func TestCompileEventSelectorBase64(t *testing.T) {
	raw, err := hex.DecodeString("979289a2")
	require.NoError(t, err)
	def, err := compileEvent(&EventConfig{
		Name:     "swap",
		Selector: "l5KJog==", // base64 of 0x979289a2
		Args:     []EventArgConfig{{Type: "uint64"}},
	})
	require.NoError(t, err)
	assert.Equal(t, raw, def.selector[:])
}

func TestCompileEventSelectorMatchesSignature(t *testing.T) {
	// Matching selector and signature are accepted.
	def, err := compileEvent(&EventConfig{
		Name:      "transfer",
		Signature: "Transfer(address,address,uint64)",
		Selector:  "0xb1e134ac",
	})
	require.NoError(t, err)
	assert.Equal(t, "b1e134ac", hex.EncodeToString(def.selector[:]))

	// A mismatching selector is rejected.
	_, err = compileEvent(&EventConfig{
		Name:      "transfer",
		Signature: "Transfer(address,address,uint64)",
		Selector:  "0xdeadbeef",
	})
	assert.ErrorContains(t, err, "does not match")
}

func TestCompileEventDefaultArgNames(t *testing.T) {
	def, err := compileEvent(&EventConfig{
		Name:      "e",
		Signature: "E(uint64,bool)",
	})
	require.NoError(t, err)
	require.Len(t, def.args, 2)
	assert.Equal(t, "arg_0", def.args[0].colName)
	assert.Equal(t, "arg_1", def.args[1].colName)
}

func TestCompileEventZeroArgs(t *testing.T) {
	def, err := compileEvent(&EventConfig{
		Name:      "e",
		Signature: "Ping()",
	})
	require.NoError(t, err)
	assert.Empty(t, def.args)
	assert.Equal(t, selectorVector(t, "Ping()"), def.selector)
}

func TestCompileEventErrors(t *testing.T) {
	testCases := []struct {
		name    string
		cfg     EventConfig
		errPart string
	}{
		{"no marker", EventConfig{Name: "e"}, "requires a signature or a selector"},
		{"bad name", EventConfig{Name: "bad-name", Signature: "E()"}, "must match"},
		{"bad signature", EventConfig{Name: "e", Signature: "no-parens"}, "invalid signature"},
		{"return type", EventConfig{Name: "e", Signature: "E()void"}, "must not include a return type"},
		{"bad arg type", EventConfig{Name: "e", Signature: "E(nonsense)"}, "invalid argument type"},
		{"bad selector", EventConfig{Name: "e", Selector: "!!"}, "invalid selector"},
		{"selector wrong length", EventConfig{Name: "e", Selector: "0x0102"}, "must be 4 bytes"},
		{"selector missing arg type", EventConfig{Name: "e", Selector: "0x01020304", Args: []EventArgConfig{{Name: "a"}}}, "requires a type"},
		{"arg count mismatch", EventConfig{Name: "e", Signature: "E(uint64)", Args: []EventArgConfig{{Name: "a"}, {Name: "b"}}}, "2 in its args list"},
		{"arg type mismatch", EventConfig{Name: "e", Signature: "E(uint64)", Args: []EventArgConfig{{Type: "string"}}}, "does not match signature type"},
		{"bad arg name", EventConfig{Name: "e", Signature: "E(uint64)", Args: []EventArgConfig{{Name: "0bad"}}}, "must match"},
		{"duplicate arg name", EventConfig{Name: "e", Signature: "E(uint64,uint64)", Args: []EventArgConfig{{Name: "a"}, {Name: "A"}}}, "duplicate argument column"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileEvent(&tc.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errPart)
		})
	}
}

func TestClassifyABIType(t *testing.T) {
	testCases := []struct {
		abiType   string
		sqlType   string
		kind      argKind
		precision uint16
	}{
		{"bool", "BOOLEAN", kindBool, 0},
		{"byte", "SMALLINT", kindUint8, 0},
		{"uint8", "SMALLINT", kindUint8, 0},
		{"uint16", "INTEGER", kindUint16, 0},
		{"uint32", "BIGINT", kindUint32, 0},
		{"uint64", "NUMERIC(20,0)", kindUint64, 0},
		{"uint128", "NUMERIC", kindUintBig, 0},
		{"uint512", "NUMERIC", kindUintBig, 0},
		{"ufixed64x3", "NUMERIC", kindUfixed, 3},
		{"ufixed128x10", "NUMERIC", kindUfixed, 10},
		{"string", "TEXT", kindString, 0},
		{"address", "TEXT", kindAddress, 0},
		{"byte[]", "BYTEA", kindBytes, 0},
		{"byte[32]", "BYTEA", kindBytes, 0},
		{"uint64[]", "JSONB", kindJSON, 0},
		{"uint16[3]", "JSONB", kindJSON, 0},
		{"(uint64,string)", "JSONB", kindJSON, 0},
		{"bool[2]", "JSONB", kindJSON, 0},
	}
	for _, tc := range testCases {
		t.Run(tc.abiType, func(t *testing.T) {
			abiType, err := abi.TypeOf(tc.abiType)
			require.NoError(t, err)
			sqlType, kind, precision, err := classifyABIType(abiType)
			require.NoError(t, err)
			assert.Equal(t, tc.sqlType, sqlType)
			assert.Equal(t, tc.kind, kind)
			assert.Equal(t, tc.precision, precision)
		})
	}
}

// encodeArgs ABI encodes values as an event payload using the SDK.
func encodeArgs(t *testing.T, types []string, values ...interface{}) []byte {
	t.Helper()
	require.Len(t, types, len(values))
	abiTypes := make([]abi.Type, len(types))
	for i, typeStr := range types {
		abiType, err := abi.TypeOf(typeStr)
		require.NoError(t, err)
		abiTypes[i] = abiType
	}
	tuple, err := abi.MakeTupleType(abiTypes)
	require.NoError(t, err)
	encoded, err := tuple.Encode(values)
	require.NoError(t, err)
	return encoded
}

func makeEventDef(t *testing.T, signature string) *eventDef {
	t.Helper()
	def, err := compileEvent(&EventConfig{Name: "e", Signature: signature})
	require.NoError(t, err)
	return def
}

func TestDecodeArgsScalars(t *testing.T) {
	def := makeEventDef(t, "E(bool,uint8,uint16,uint32,uint64,string,address)")
	addressBytes := make([]byte, 32)
	for i := range addressBytes {
		addressBytes[i] = byte(i)
	}
	payload := encodeArgs(t,
		[]string{"bool", "uint8", "uint16", "uint32", "uint64", "string", "address"},
		true, uint8(7), uint16(300), uint32(70000), uint64(18446744073709551615), "hello", addressBytes)
	values, err := def.decodeArgs(payload)
	require.NoError(t, err)
	require.Len(t, values, 7)
	assert.Equal(t, true, values[0])
	assert.Equal(t, int16(7), values[1])
	assert.Equal(t, int32(300), values[2])
	assert.Equal(t, int64(70000), values[3])
	// uint64 max is preserved exactly through the NUMERIC column.
	numeric, ok := values[4].(pgtype.Numeric)
	require.True(t, ok)
	assert.Equal(t, "18446744073709551615", numeric.Int.String())
	assert.Equal(t, "hello", values[5])
	var expectedAddress sdk.Address
	copy(expectedAddress[:], addressBytes)
	assert.Equal(t, expectedAddress.String(), values[6])
	assert.Len(t, values[6], 58)
}

func TestDecodeArgsUfixed(t *testing.T) {
	def := makeEventDef(t, "E(ufixed64x3,ufixed128x10)")
	bigVal := new(big.Int).Exp(big.NewInt(2), big.NewInt(100), nil)
	payload := encodeArgs(t,
		[]string{"ufixed64x3", "ufixed128x10"},
		uint64(123456), bigVal)
	values, err := def.decodeArgs(payload)
	require.NoError(t, err)
	require.Len(t, values, 2)
	small := values[0].(pgtype.Numeric)
	// 123456 / 10^3 = 123.456
	assert.Equal(t, "123456", small.Int.String())
	assert.Equal(t, int32(-3), small.Exp)
	large := values[1].(pgtype.Numeric)
	assert.Equal(t, bigVal.String(), large.Int.String())
	assert.Equal(t, int32(-10), large.Exp)
}

func TestDecodeArgsBigUint(t *testing.T) {
	def := makeEventDef(t, "E(uint128)")
	bigVal := new(big.Int).Exp(big.NewInt(2), big.NewInt(100), nil)
	payload := encodeArgs(t, []string{"uint128"}, bigVal)
	values, err := def.decodeArgs(payload)
	require.NoError(t, err)
	numeric := values[0].(pgtype.Numeric)
	assert.Equal(t, bigVal.String(), numeric.Int.String())
}

func TestDecodeArgsByteArrays(t *testing.T) {
	def := makeEventDef(t, "E(byte[],byte[4])")
	payload := encodeArgs(t,
		[]string{"byte[]", "byte[4]"},
		[]byte{1, 2, 3}, []byte{9, 8, 7, 6})
	values, err := def.decodeArgs(payload)
	require.NoError(t, err)
	assert.Equal(t, []byte{1, 2, 3}, values[0])
	assert.Equal(t, []byte{9, 8, 7, 6}, values[1])
}

func TestDecodeArgsCompositeToJSON(t *testing.T) {
	def := makeEventDef(t, "E(uint64[],(string,bool))")
	payload := encodeArgs(t,
		[]string{"uint64[]", "(string,bool)"},
		[]uint64{1, 2, 3}, []interface{}{"hi", true})
	values, err := def.decodeArgs(payload)
	require.NoError(t, err)
	require.Len(t, values, 2)
	arrayJSON := values[0].(pgtype.JSONB)
	assert.JSONEq(t, `[1,2,3]`, string(arrayJSON.Bytes))
	tupleJSON := values[1].(pgtype.JSONB)
	assert.JSONEq(t, `["hi",true]`, string(tupleJSON.Bytes))
}

func TestDecodeArgsZeroArgs(t *testing.T) {
	def := makeEventDef(t, "E()")
	values, err := def.decodeArgs(nil)
	require.NoError(t, err)
	assert.Empty(t, values)

	// Trailing garbage is rejected.
	_, err = def.decodeArgs([]byte{1})
	assert.Error(t, err)
}

func TestDecodeArgsBadPayloads(t *testing.T) {
	testCases := []struct {
		name      string
		signature string
		payload   []byte
	}{
		{"truncated uint", "E(uint64)", []byte{1, 2, 3}},
		{"truncated bool", "E(bool)", []byte{}},
		{"truncated bool group", "E(bool,bool,bool,bool,bool,bool,bool,bool,bool)", []byte{0x80}},
		{"truncated tuple", "E(uint64,uint64)", make([]byte, 9)},
		{"bad string length", "E(string)", []byte{0, 5, 'a'}},
		{"bad dynamic offset", "E(uint64[])", []byte{0xff, 0xff}},
		{"trailing garbage", "E(uint64)", make([]byte, 9)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			def := makeEventDef(t, tc.signature)
			// Must return an error, not panic.
			_, err := def.decodeArgs(tc.payload)
			assert.Error(t, err)
		})
	}
}

func TestParseSelector(t *testing.T) {
	sel, err := parseSelector("0xb1e134ac")
	require.NoError(t, err)
	assert.Equal(t, "b1e134ac", hex.EncodeToString(sel[:]))

	sel, err = parseSelector("b1e134ac")
	require.NoError(t, err)
	assert.Equal(t, "b1e134ac", hex.EncodeToString(sel[:]))

	_, err = parseSelector("0x1234")
	assert.ErrorContains(t, err, "must be 4 bytes")

	_, err = parseSelector("not a selector at all")
	assert.Error(t, err)
}
