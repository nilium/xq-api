package main

import "testing"

func TestParseVersionedName(t *testing.T) {
	cases := []struct {
		vsn           string
		name, version string
		revision      int
	}{
		{
			vsn:  "mac-32bit-3.99u4b5s7_2",
			name: "mac-32bit", version: "3.99u4b5s7",
			revision: 2,
		},
		{
			vsn:  "navit-32bit-0.5.1+rc1_1",
			name: "navit-32bit", version: "0.5.1+rc1",
			revision: 1,
		},
		{
			vsn:  "occt-32bit-7.2.0p1_1",
			name: "occt-32bit", version: "7.2.0p1",
			revision: 1,
		},
		{
			vsn:  "occt-devel-32bit-7.2.0p1_1",
			name: "occt-devel-32bit", version: "7.2.0p1",
			revision: 1,
		},
		{
			vsn:  "openjdk-jre-32bit-8u182b00_1",
			name: "openjdk-jre-32bit", version: "8u182b00",
			revision: 1,
		},
		{
			vsn:  "qpdfview-32bit-0.4.17beta1_1",
			name: "qpdfview-32bit", version: "0.4.17beta1",
			revision: 1,
		},
		{
			vsn:  "telepathy-mission-control-32bit-5:5.16.1_2",
			name: "telepathy-mission-control-32bit", version: "5:5.16.1",
			revision: 2,
		},
		{
			vsn:  "tsocks-32bit-1.8beta5_3",
			name: "tsocks-32bit", version: "1.8beta5",
			revision: 3,
		},
		{
			vsn:  "vapoursynth-32bit-R43_1",
			name: "vapoursynth-32bit", version: "R43",
			revision: 1,
		},
	}

	for _, c := range cases {
		name, version, rev, err := ParseVersionedName(c.vsn)
		if err != nil {
			t.Fatalf("cannot parse %q: %v", c.vsn, err)
		}

		if name != c.name {
			t.Errorf("name = %q; want %q", name, c.name)
		}

		if version != c.version {
			t.Errorf("version = %q; want %q", version, c.version)
		}

		if rev != c.revision {
			t.Errorf("revision = %d; want %d", rev, c.revision)
		}

		if t.Failed() {
			return
		}
	}
}
