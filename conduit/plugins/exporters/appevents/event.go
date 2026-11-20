package appevents

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgtype"

	avmabi "github.com/algorand/avm-abi/abi"
	"github.com/algorand/go-algorand-sdk/v2/abi"
	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// selectorLength is the length of an ARC-28 style event marker.
const selectorLength = 4

// argKind classifies ABI argument types for conversion to SQL values.
type argKind int

const (
	kindBool argKind = iota
	kindUint8
	kindUint16
	kindUint32
	kindUint64
	kindUintBig
	kindUfixed
	kindString
	kindAddress
	kindBytes
	kindJSON
)

// eventArg is a single compiled event argument: one typed table column.
type eventArg struct {
	// colName is the SQL column name, "arg_" prefixed.
	colName string
	// sqlType is the SQL column type used in DDL.
	sqlType string
	// abiType is the parsed ABI type.
	abiType abi.Type
	// kind drives value conversion.
	kind argKind
	// precision is the ufixed scale (only for kindUfixed).
	precision uint16
}

// eventDef is a compiled event definition.
type eventDef struct {
	// name is the configured event name.
	name string
	// table is the event's SQL table name.
	table string
	// signature is the canonical event signature, empty for selector-only events.
	signature string
	// selector is the 4 byte event marker.
	selector [selectorLength]byte
	// args are the typed event argument columns.
	args []eventArg
	// tuple is the ABI tuple type of the arguments, used to decode payloads.
	tuple abi.Type
}

var (
	uintTypeRe      = regexp.MustCompile(`^uint([0-9]+)$`)
	ufixedTypeRe    = regexp.MustCompile(`^ufixed([0-9]+)x([0-9]+)$`)
	byteArrayTypeRe = regexp.MustCompile(`^byte\[[0-9]*\]$`)
)

// classifyABIType maps an ABI type to its SQL column type and value
// conversion kind. The canonical type string produced by the SDK is used so
// that aliases (e.g. "byte" for "uint8") normalize consistently.
func classifyABIType(t abi.Type) (sqlType string, kind argKind, precision uint16, err error) {
	s := t.String()
	switch s {
	case "bool":
		return "BOOLEAN", kindBool, 0, nil
	case "byte":
		return "SMALLINT", kindUint8, 0, nil
	case "string":
		return "TEXT", kindString, 0, nil
	case "address":
		return "TEXT", kindAddress, 0, nil
	}
	if m := uintTypeRe.FindStringSubmatch(s); m != nil {
		bits, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			return "", 0, 0, fmt.Errorf("unable to parse uint type %q: %w", s, convErr)
		}
		switch {
		case bits <= 8:
			return "SMALLINT", kindUint8, 0, nil
		case bits <= 16:
			return "INTEGER", kindUint16, 0, nil
		case bits <= 32:
			return "BIGINT", kindUint32, 0, nil
		case bits <= 64:
			// uint64 does not fit in a signed BIGINT.
			return "NUMERIC(20,0)", kindUint64, 0, nil
		default:
			return "NUMERIC", kindUintBig, 0, nil
		}
	}
	if m := ufixedTypeRe.FindStringSubmatch(s); m != nil {
		prec, convErr := strconv.Atoi(m[2])
		if convErr != nil {
			return "", 0, 0, fmt.Errorf("unable to parse ufixed type %q: %w", s, convErr)
		}
		return "NUMERIC", kindUfixed, uint16(prec), nil
	}
	if byteArrayTypeRe.MatchString(s) {
		return "BYTEA", kindBytes, 0, nil
	}
	// Remaining composite types (tuples, non-byte arrays) are stored as JSON.
	return "JSONB", kindJSON, 0, nil
}

// parseSelector parses an explicitly configured 4 byte event marker, hex
// (optional 0x prefix) or base64 encoded.
func parseSelector(s string) ([selectorLength]byte, error) {
	var selector [selectorLength]byte
	trimmed := strings.TrimSpace(s)
	hexStr := strings.TrimPrefix(trimmed, "0x")
	if decoded, err := hex.DecodeString(hexStr); err == nil {
		if len(decoded) != selectorLength {
			return selector, fmt.Errorf("selector must be %d bytes, got %d", selectorLength, len(decoded))
		}
		copy(selector[:], decoded)
		return selector, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		if len(decoded) != selectorLength {
			return selector, fmt.Errorf("selector must be %d bytes, got %d", selectorLength, len(decoded))
		}
		copy(selector[:], decoded)
		return selector, nil
	}
	return selector, fmt.Errorf("selector %q is neither hex nor base64 encoded", s)
}

// eventSelector computes the ARC-28 event marker: the first 4 bytes of the
// sha512/256 hash of the canonical event signature.
func eventSelector(canonicalSignature string) [selectorLength]byte {
	var selector [selectorLength]byte
	hash := sha512.Sum512_256([]byte(canonicalSignature))
	copy(selector[:], hash[:selectorLength])
	return selector
}

// compileEvent validates an EventConfig and compiles it into an eventDef.
func compileEvent(cfg *EventConfig) (*eventDef, error) {
	if !identifierRegexp.MatchString(cfg.Name) {
		return nil, fmt.Errorf("event name %q must match %s", cfg.Name, identifierRegexp.String())
	}
	if cfg.Signature == "" && cfg.Selector == "" {
		return nil, fmt.Errorf("event %q requires a signature or a selector", cfg.Name)
	}

	def := &eventDef{
		name:  cfg.Name,
		table: eventTablePrefix + strings.ToLower(cfg.Name),
	}

	var argTypes []abi.Type
	if cfg.Signature != "" {
		name, argTypeStrs, retType, err := avmabi.ParseMethodSignature(cfg.Signature)
		if err != nil {
			return nil, fmt.Errorf("event %q has an invalid signature: %w", cfg.Name, err)
		}
		if retType != "" {
			return nil, fmt.Errorf("event %q signature must not include a return type", cfg.Name)
		}
		canonical := make([]string, 0, len(argTypeStrs))
		for _, typeStr := range argTypeStrs {
			t, err := abi.TypeOf(strings.TrimSpace(typeStr))
			if err != nil {
				return nil, fmt.Errorf("event %q has an invalid argument type %q: %w", cfg.Name, typeStr, err)
			}
			argTypes = append(argTypes, t)
			canonical = append(canonical, t.String())
		}
		def.signature = name + "(" + strings.Join(canonical, ",") + ")"
		def.selector = eventSelector(def.signature)
	}
	if cfg.Selector != "" {
		selector, err := parseSelector(cfg.Selector)
		if err != nil {
			return nil, fmt.Errorf("event %q has an invalid selector: %w", cfg.Name, err)
		}
		if cfg.Signature != "" && selector != def.selector {
			return nil, fmt.Errorf("event %q selector 0x%x does not match its signature marker 0x%x", cfg.Name, selector, def.selector)
		}
		def.selector = selector
	}

	if argTypes == nil {
		// Selector-only event: argument types come from the args list.
		argTypes = make([]abi.Type, 0, len(cfg.Args))
		for i, argCfg := range cfg.Args {
			if argCfg.Type == "" {
				return nil, fmt.Errorf("event %q argument %d requires a type when no signature is given", cfg.Name, i)
			}
			t, err := abi.TypeOf(argCfg.Type)
			if err != nil {
				return nil, fmt.Errorf("event %q argument %d has an invalid type %q: %w", cfg.Name, i, argCfg.Type, err)
			}
			argTypes = append(argTypes, t)
		}
	} else if len(cfg.Args) > 0 {
		// Signature event: args may only add names, and any type given must match.
		if len(cfg.Args) != len(argTypes) {
			return nil, fmt.Errorf("event %q has %d arguments in its signature but %d in its args list", cfg.Name, len(argTypes), len(cfg.Args))
		}
		for i, argCfg := range cfg.Args {
			if argCfg.Type == "" {
				continue
			}
			t, err := abi.TypeOf(argCfg.Type)
			if err != nil {
				return nil, fmt.Errorf("event %q argument %d has an invalid type %q: %w", cfg.Name, i, argCfg.Type, err)
			}
			if t.String() != argTypes[i].String() {
				return nil, fmt.Errorf("event %q argument %d type %q does not match signature type %q", cfg.Name, i, argCfg.Type, argTypes[i].String())
			}
		}
	}

	argNames := make(map[string]struct{})
	for i, t := range argTypes {
		name := strconv.Itoa(i)
		if i < len(cfg.Args) && cfg.Args[i].Name != "" {
			name = cfg.Args[i].Name
			if !identifierRegexp.MatchString(name) {
				return nil, fmt.Errorf("event %q argument name %q must match %s", cfg.Name, name, identifierRegexp.String())
			}
		}
		colName := "arg_" + strings.ToLower(name)
		if _, ok := argNames[colName]; ok {
			return nil, fmt.Errorf("event %q has a duplicate argument column %q", cfg.Name, colName)
		}
		argNames[colName] = struct{}{}
		sqlType, kind, precision, err := classifyABIType(t)
		if err != nil {
			return nil, fmt.Errorf("event %q argument %q: %w", cfg.Name, name, err)
		}
		def.args = append(def.args, eventArg{
			colName:   colName,
			sqlType:   sqlType,
			abiType:   t,
			kind:      kind,
			precision: precision,
		})
	}

	tuple, err := abi.MakeTupleType(argTypes)
	if err != nil {
		return nil, fmt.Errorf("event %q: unable to build argument tuple type: %w", cfg.Name, err)
	}
	def.tuple = tuple
	return def, nil
}

// decodeArgs ABI decodes a log payload (the log bytes after the 4 byte
// marker) into SQL-ready argument values.
func (def *eventDef) decodeArgs(payload []byte) ([]interface{}, error) {
	decoded, err := safeDecode(def.tuple, payload)
	if err != nil {
		return nil, err
	}
	items, ok := decoded.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected decoded value of type %T", decoded)
	}
	if len(items) != len(def.args) {
		return nil, fmt.Errorf("decoded %d values for %d arguments", len(items), len(def.args))
	}
	values := make([]interface{}, len(def.args))
	for i := range def.args {
		value, err := def.args[i].sqlValue(items[i])
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", def.args[i].colName, err)
		}
		values[i] = value
	}
	return values, nil
}

// safeDecode decodes an ABI value, converting SDK decoder panics on
// malformed input into errors so that bad logs cannot crash the exporter.
func safeDecode(t abi.Type, payload []byte) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("abi decode failure: %v", r)
		}
	}()
	return t.Decode(payload)
}

// sqlValue converts a decoded ABI value into a driver value matching the
// column's SQL type.
func (a *eventArg) sqlValue(v interface{}) (interface{}, error) {
	switch a.kind {
	case kindBool:
		value, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool, got %T", v)
		}
		return value, nil
	case kindUint8:
		value, ok := v.(uint8)
		if !ok {
			return nil, fmt.Errorf("expected uint8, got %T", v)
		}
		return int16(value), nil
	case kindUint16:
		value, ok := v.(uint16)
		if !ok {
			return nil, fmt.Errorf("expected uint16, got %T", v)
		}
		return int32(value), nil
	case kindUint32:
		value, ok := v.(uint32)
		if !ok {
			return nil, fmt.Errorf("expected uint32, got %T", v)
		}
		return int64(value), nil
	case kindUint64:
		value, ok := v.(uint64)
		if !ok {
			return nil, fmt.Errorf("expected uint64, got %T", v)
		}
		return numericValue(strconv.FormatUint(value, 10))
	case kindUintBig:
		value, ok := v.(*big.Int)
		if !ok {
			return nil, fmt.Errorf("expected *big.Int, got %T", v)
		}
		return numericValue(value.String())
	case kindUfixed:
		value, err := toBigInt(v)
		if err != nil {
			return nil, err
		}
		denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(a.precision)), nil)
		scaled := new(big.Rat).SetFrac(value, denominator)
		return numericValue(scaled.FloatString(int(a.precision)))
	case kindString:
		value, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", v)
		}
		return value, nil
	case kindAddress:
		value, ok := v.([]byte)
		if !ok {
			return nil, fmt.Errorf("expected address bytes, got %T", v)
		}
		if len(value) != 32 {
			return nil, fmt.Errorf("expected 32 address bytes, got %d", len(value))
		}
		var address sdk.Address
		copy(address[:], value)
		return address.String(), nil
	case kindBytes:
		items, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("expected byte array, got %T", v)
		}
		value := make([]byte, len(items))
		for i, item := range items {
			b, ok := item.(uint8)
			if !ok {
				return nil, fmt.Errorf("expected byte element, got %T", item)
			}
			value[i] = b
		}
		return value, nil
	case kindJSON:
		encoded, err := a.abiType.MarshalToJSON(v)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal value to JSON: %w", err)
		}
		return pgtype.JSONB{Bytes: encoded, Status: pgtype.Present}, nil
	}
	return nil, fmt.Errorf("unknown argument kind %d", a.kind)
}

// numericValue builds a pgtype.Numeric from its decimal string form.
func numericValue(decimal string) (pgtype.Numeric, error) {
	var value pgtype.Numeric
	if err := value.Set(decimal); err != nil {
		return value, fmt.Errorf("invalid numeric value %q: %w", decimal, err)
	}
	return value, nil
}

// toBigInt converts SDK decoded integer values to *big.Int.
func toBigInt(v interface{}) (*big.Int, error) {
	switch value := v.(type) {
	case uint8:
		return new(big.Int).SetUint64(uint64(value)), nil
	case uint16:
		return new(big.Int).SetUint64(uint64(value)), nil
	case uint32:
		return new(big.Int).SetUint64(uint64(value)), nil
	case uint64:
		return new(big.Int).SetUint64(value), nil
	case *big.Int:
		return value, nil
	}
	return nil, fmt.Errorf("expected integer value, got %T", v)
}
