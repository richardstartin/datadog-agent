// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

package pb

import (
	"errors"
	fmt "fmt"
	io "io"
	"math"
	"sync"

	"github.com/philhofer/fwd"
	"github.com/tinylib/msgp/msgp"
)

// parseString reads the next type in the msgpack payload and
// converts the BinType or the StrType in a valid string.
func parseString(dc *msgp.Reader) (string, error) {
	// read the generic representation type without decoding
	t, err := dc.NextType()
	if err != nil {
		return "", err
	}
	switch t {
	case msgp.BinType:
		i, err := dc.ReadBytes(nil)
		if err != nil {
			return "", err
		}
		return msgp.UnsafeString(i), nil
	case msgp.StrType:
		i, err := dc.ReadString()
		if err != nil {
			return "", err
		}
		return i, nil
	default:
		return "", msgp.TypeError{Encoded: t, Method: msgp.StrType}
	}
}

// parseStringDict
func parseStringDict(dc *msgp.Reader, dict []string) (string, error) {
	i, err := dc.ReadInt()
	if i >= len(dict) {
		return "", fmt.Errorf("dictionary index %d out of place", i)
	}
	return dict[i], err
}

// parseFloat64 parses a float64 even if the sent value is an int64 or an uint64;
// this is required because the encoding library could remove bytes from the encoded
// payload to reduce the size, if they're not needed.
func parseFloat64(dc *msgp.Reader) (float64, error) {
	// read the generic representation type without decoding
	t, err := dc.NextType()
	if err != nil {
		return 0, err
	}

	switch t {
	case msgp.IntType:
		i, err := dc.ReadInt64()
		if err != nil {
			return 0, err
		}

		return float64(i), nil
	case msgp.UintType:
		i, err := dc.ReadUint64()
		if err != nil {
			return 0, err
		}

		return float64(i), nil
	case msgp.Float64Type:
		f, err := dc.ReadFloat64()
		if err != nil {
			return 0, err
		}

		return f, nil
	default:
		return 0, msgp.TypeError{Encoded: t, Method: msgp.Float64Type}
	}
}

// cast to int64 values that are int64 but that are sent in uint64
// over the wire. Set to 0 if they overflow the MaxInt64 size. This
// cast should be used ONLY while decoding int64 values that are
// sent as uint64 to reduce the payload size, otherwise the approach
// is not correct in the general sense.
func castInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return 0, false
	}

	return int64(v), true
}

// parseInt64 parses an int64 even if the sent value is an uint64;
// this is required because the encoding library could remove bytes from the encoded
// payload to reduce the size, if they're not needed.
func parseInt64(dc *msgp.Reader) (int64, error) {
	// read the generic representation type without decoding
	t, err := dc.NextType()
	if err != nil {
		return 0, err
	}

	switch t {
	case msgp.IntType:
		i, err := dc.ReadInt64()
		if err != nil {
			return 0, err
		}
		return i, nil
	case msgp.UintType:
		u, err := dc.ReadUint64()
		if err != nil {
			return 0, err
		}

		// force-cast
		i, ok := castInt64(u)
		if !ok {
			return 0, errors.New("found uint64, overflows int64")
		}
		return i, nil
	default:
		return 0, msgp.TypeError{Encoded: t, Method: msgp.IntType}
	}
}

// parseUint64 parses an uint64 even if the sent value is an int64;
// this is required because the language used for the encoding library
// may not have unsigned types. An example is early version of Java
// (and so JRuby interpreter) that encodes uint64 as int64:
// http://docs.oracle.com/javase/tutorial/java/nutsandbolts/datatypes.html
func parseUint64(dc *msgp.Reader) (uint64, error) {
	// read the generic representation type without decoding
	t, err := dc.NextType()
	if err != nil {
		return 0, err
	}

	switch t {
	case msgp.UintType:
		u, err := dc.ReadUint64()
		if err != nil {
			return 0, err
		}
		return u, err
	case msgp.IntType:
		i, err := dc.ReadInt64()
		if err != nil {
			return 0, err
		}
		return uint64(i), nil
	default:
		return 0, msgp.TypeError{Encoded: t, Method: msgp.IntType}
	}
}

// cast to int32 values that are int32 but that are sent in uint32
// over the wire. Set to 0 if they overflow the MaxInt32 size. This
// cast should be used ONLY while decoding int32 values that are
// sent as uint32 to reduce the payload size, otherwise the approach
// is not correct in the general sense.
func castInt32(v uint32) (int32, bool) {
	if v > math.MaxInt32 {
		return 0, false
	}

	return int32(v), true
}

// parseInt32 parses an int32 even if the sent value is an uint32;
// this is required because the encoding library could remove bytes from the encoded
// payload to reduce the size, if they're not needed.
func parseInt32(dc *msgp.Reader) (int32, error) {
	// read the generic representation type without decoding
	t, err := dc.NextType()
	if err != nil {
		return 0, err
	}

	switch t {
	case msgp.IntType:
		i, err := dc.ReadInt32()
		if err != nil {
			return 0, err
		}
		return i, nil
	case msgp.UintType:
		u, err := dc.ReadUint32()
		if err != nil {
			return 0, err
		}

		// force-cast
		i, ok := castInt32(u)
		if !ok {
			return 0, errors.New("found uint32, overflows int32")
		}
		return i, nil
	default:
		return 0, msgp.TypeError{Encoded: t, Method: msgp.IntType}
	}
}

// DecodeMsgArray implements msgp.Decodable
func (z *Traces) DecodeMsgArray(dc *msgp.Reader) (err error) {
	if _, err := dc.ReadArrayHeader(); err != nil {
		return err
	}
	// read dictionary
	sz, err := dc.ReadArrayHeader()
	if err != nil {
		return err
	}
	dict := make([]string, sz)
	for i := range dict {
		nextType, err := dc.NextType()
		if err != nil {
			return err
		}

		switch nextType {
		case msgp.NilType:
			// we don't like nil very much, so we'll replace it with
			// the empty string for downstream consumers. Handling this
			// here is nice though, because we enforce the policy for all
			// tracers.
			dict[i] = ""
			break
		case msgp.BinType:
			bytes, err := dc.ReadBytes(nil)
			if err != nil {
				return err
			}
			dict[i] = msgp.UnsafeString(bytes)
			break
		case msgp.StrType:
			utf8, err := dc.ReadString()
			if err != nil {
				return err
			}
			dict[i] = utf8
			break
		default:
			return fmt.Errorf("dictionary value at index %d has unsupported type", i)
		}
	}
	// read traces
	var xsz uint32
	xsz, err = dc.ReadArrayHeader()
	if err != nil {
		return
	}
	if cap(*z) >= int(xsz) {
		*z = (*z)[:xsz]
	} else {
		*z = make(Traces, xsz)
	}
	for wht := range *z {
		var xsz uint32
		xsz, err = dc.ReadArrayHeader()
		if err != nil {
			return
		}
		if cap((*z)[wht]) >= int(xsz) {
			(*z)[wht] = (*z)[wht][:xsz]
		} else {
			(*z)[wht] = make(Trace, xsz)
		}
		for hct := range (*z)[wht] {
			if (*z)[wht][hct] == nil {
				(*z)[wht][hct] = new(Span)
			}
			err = (*z)[wht][hct].DecodeMsgArray(dc, dict)
			if err != nil {
				return
			}
		}
	}
	return
}

const spanPropertyCount = 12

// DecodeMsgArray implements msgp.Decodable
func (z *Span) DecodeMsgArray(decoder *msgp.Reader, dictionary []string) (err error) {
	var xsz uint32
	xsz, err = decoder.ReadArrayHeader()
	if err != nil {
		return
	}
	if xsz != spanPropertyCount {
		return errors.New("encoded z needs exactly 12 elements in array")
	}

	// Service (0)
	z.Service, err = parseStringDict(decoder, dictionary)
	if err != nil {
		return
	}
	// Name (1)
	z.Name, err = parseStringDict(decoder, dictionary)
	if err != nil {
		return
	}
	// Resource (2)
	z.Resource, err = parseStringDict(decoder, dictionary)
	if err != nil {
		return
	}
	// TraceID (3)
	z.TraceID, err = parseUint64(decoder)
	if err != nil {
		return
	}
	// SpanID (4)
	z.SpanID, err = parseUint64(decoder)
	if err != nil {
		return
	}
	// ParentID (5)
	z.ParentID, err = parseUint64(decoder)
	if err != nil {
		return
	}
	// Start (6)
	z.Start, err = parseInt64(decoder)
	if err != nil {
		return
	}
	// Duration (7)
	z.Duration, err = parseInt64(decoder)
	if err != nil {
		return
	}
	// Error (8)
	z.Error, err = parseInt32(decoder)
	if err != nil {
		return
	}
	// Meta (9)
	var metaSize uint32
	metaSize, err = decoder.ReadMapHeader()
	if err != nil {
		return
	}
	if z.Meta == nil && metaSize > 0 {
		z.Meta = make(map[string]string, metaSize)
	} else if len(z.Meta) > 0 {
		for key := range z.Meta {
			delete(z.Meta, key)
		}
	}
	for metaSize > 0 {
		metaSize--
		var zxvk string
		var zbzg string
		zxvk, err = parseStringDict(decoder, dictionary)
		if err != nil {
			return
		}
		zbzg, err = parseStringDict(decoder, dictionary)
		if err != nil {
			return
		}
		z.Meta[zxvk] = zbzg
	}
	// Metrics (10)
	var metricsSize uint32
	metricsSize, err = decoder.ReadMapHeader()
	if err != nil {
		return
	}
	if z.Metrics == nil && metricsSize > 0 {
		z.Metrics = make(map[string]float64, metricsSize)
	} else if len(z.Metrics) > 0 {
		for key := range z.Metrics {
			delete(z.Metrics, key)
		}
	}
	for metricsSize > 0 {
		metricsSize--
		var zbai string
		var zcmr float64
		zbai, err = parseStringDict(decoder, dictionary)
		if err != nil {
			return
		}
		zcmr, err = parseFloat64(decoder)
		if err != nil {
			return
		}
		z.Metrics[zbai] = zcmr
	}
	// Type (11)
	z.Type, err = parseStringDict(decoder, dictionary)
	if err != nil {
		return
	}
	return nil
}

var readerPool = sync.Pool{New: func() interface{} { return &msgp.Reader{} }}

// NewMsgpReader returns a *msgp.Reader that
// reads from the provided reader. The
// reader will be buffered.
func NewMsgpReader(r io.Reader) *msgp.Reader {
	p := readerPool.Get().(*msgp.Reader)
	if p.R == nil {
		p.R = fwd.NewReader(r)
	} else {
		p.R.Reset(r)
	}
	return p
}

// FreeMsgpReader marks reader r as done.
func FreeMsgpReader(r *msgp.Reader) { readerPool.Put(r) }
