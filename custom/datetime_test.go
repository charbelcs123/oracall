// Copyright 2014, 2021 Tamás Gulácsi
//
// SPDX-License-Identifier: Apache-2.0

package custom_test

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/tgulacsi/oracall/custom"
)

func TestDateTimeMarshalXML(t *testing.T) {
	var buf strings.Builder
	enc := xml.NewEncoder(&buf)
	st := xml.StartElement{Name: xml.Name{Local: "element"}}
	for _, tC := range []struct {
		In   custom.DateTime
		Want string
	}{
		{In: custom.DateTime{}, Want: `<element xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true"></element>`},
		{In: custom.DateTime{Time: time.Date(2019, 10, 22, 16, 56, 32, 0, time.Local)}, Want: `<element>2019-10-22T16:56:32+02:00</element>`},
	} {
		buf.Reset()
		if err := tC.In.MarshalXML(enc, st); err != nil {
			t.Fatalf("%v: %+v", tC.In, err)
		}
		if got := buf.String(); tC.Want != got {
			t.Errorf("%v: got %q wanted %q", tC.In, got, tC.Want)
		}
	}
}
