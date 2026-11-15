package main

// Exact helper digests from the public v0.1.0-alpha.1 release. Only these
// unmarked legacy files may be adopted during a reset; arbitrary files are refused.
var legacyHelperDigests = map[string]string{
	"darwin-amd64": "ee92575cb13618efe968c2a4c1ccae5cae77c6b000c98c71307a2640d8149a8d",
	"darwin-arm64": "4fd7627f62b9e8385adc19f0502ed375b55d6d6a5248e9a1751541cbc6e6b5d9",
	"linux-amd64":  "ebabbd0bd3e851ec9755e10d0718fd8df5ac5c5721217f2c3380a829f175ff6e",
	"linux-arm64":  "ec299ac538ceafdb50c3fe6ebc9b9ffb2410f4bc14b40a05360ba29e9451d12d",
}
