package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/internal/app/builtin"
	"github.com/prusa3d/prusa-connect/internal/app/config"
	"github.com/prusa3d/prusa-connect/internal/kernel/atomicfile"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// The certificates the wizard makes, in a folder of the directory this server
// owns. The authority signs both leaves, and the printer trusts it through
// connect.der, the one custom certificate a printer loads from USB.
const (
	certsDir        = "certs"
	caCertFile      = "ca.crt"
	caKeyFile       = "ca.key"
	webCertFile     = "web.crt"
	webKeyFile      = "web.key"
	printerCertFile = "printer.crt"
	printerKeyFile  = "printer.key"
	printerCAFile   = "connect.der"
	certFileMode    = 0o644
	keyFileMode     = 0o600
	certsDirMode    = 0o750
)

// The lifetimes: ten years for the authority, and 825 days for a leaf, the
// longest a browser accepts.
const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 825 * 24 * time.Hour
	// backdate keeps a printer whose clock is a little behind from refusing a
	// certificate that is not valid yet.
	backdate = time.Hour
)

// maxCertificateNames bounds the names one request may ask for.
const maxCertificateNames = 32

// certsPath is the folder for an operator file.
func certsPath(configPath string) string {
	return filepath.Join(filepath.Dir(config.SetupFile(configPath)), certsDir)
}

// CreateCertificates makes an authority and the two certificates signed by it,
// for the names the person gave, behind the setup code. A second call replaces
// the files, so a person who corrects a name gets certificates for it.
func (f *firstRun) CreateCertificates(
	ctx context.Context, client httpapi.SetupClient, code string, names []string,
) (httpapi.SetupCertificates, error) {
	if !f.Pending() {
		return httpapi.SetupCertificates{}, fault.AuthSetupClosed.New(
			"this server has already recorded what it does about identity")
	}

	if err := f.code.admit(ctx, client, code); err != nil {
		return httpapi.SetupCertificates{}, err
	}

	hosts, err := certificateNames(names)
	if err != nil {
		return httpapi.SetupCertificates{}, err
	}

	made, err := writeCertificates(certsPath(f.configPath), hosts, time.Now())
	if err != nil {
		return httpapi.SetupCertificates{}, httpfault.Unexpected("create the certificates").Wrap(err)
	}

	f.logger.InfoContext(ctx, "the first run created certificates", "names", hosts, "folder", made.Folder)

	return made, nil
}

// certificateNames are the names a certificate is issued for: each one a host
// name or an address, with no duplicate.
func certificateNames(names []string) ([]string, error) {
	var hosts []string

	for _, name := range names {
		host := strings.ToLower(strings.TrimSpace(name))
		if host == "" || slices.Contains(hosts, host) {
			continue
		}

		if net.ParseIP(host) == nil && !validHostName(host) {
			return nil, httpfault.RequestInvalid("body.names", "host names and addresses", name)
		}

		hosts = append(hosts, host)
	}

	if len(hosts) == 0 || len(hosts) > maxCertificateNames {
		return nil, httpfault.RequestInvalid("body.names",
			fmt.Sprintf("between 1 and %d names", maxCertificateNames), "")
	}

	return hosts, nil
}

// The longest DNS name and the longest label in it (RFC 1035).
const (
	maxHostName  = 253
	maxHostLabel = 63
)

// validHostName is a DNS name: labels of letters, digits and hyphens.
func validHostName(host string) bool {
	if len(host) > maxHostName {
		return false
	}

	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > maxHostLabel ||
			strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}

		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}

	return true
}

// writeCertificates makes the authority and both leaves and writes them under
// folder.
func writeCertificates(folder string, hosts []string, now time.Time) (httpapi.SetupCertificates, error) {
	caKey, caCert, caDER, err := newAuthority(now)
	if err != nil {
		return httpapi.SetupCertificates{}, err
	}

	if err = os.MkdirAll(folder, certsDirMode); err != nil {
		return httpapi.SetupCertificates{}, fmt.Errorf("create %s: %w", folder, err)
	}

	files := map[string][]byte{caCertFile: pemOf("CERTIFICATE", caDER), printerCAFile: caDER}

	if files[caKeyFile], err = keyPEM(caKey); err != nil {
		return httpapi.SetupCertificates{}, err
	}

	for _, leaf := range []struct{ cert, key string }{
		{webCertFile, webKeyFile},
		{printerCertFile, printerKeyFile},
	} {
		certPEM, leafKeyPEM, leafErr := newLeaf(caCert, caKey, hosts, now)
		if leafErr != nil {
			return httpapi.SetupCertificates{}, leafErr
		}

		files[leaf.cert], files[leaf.key] = certPEM, leafKeyPEM
	}

	for name, body := range files {
		mode := os.FileMode(certFileMode)
		if strings.HasSuffix(name, ".key") {
			mode = keyFileMode
		}

		if err = atomicfile.Write(filepath.Join(folder, name), body, mode); err != nil {
			return httpapi.SetupCertificates{}, err
		}
	}

	absolute, err := filepath.Abs(folder)
	if err != nil {
		absolute = folder
	}

	return httpapi.SetupCertificates{
		Folder:             absolute,
		Names:              hosts,
		WebCertificate:     filepath.Join(absolute, webCertFile),
		WebKey:             filepath.Join(absolute, webKeyFile),
		PrinterCertificate: filepath.Join(absolute, printerCertFile),
		PrinterKey:         filepath.Join(absolute, printerKeyFile),
		Authority:          filepath.Join(absolute, caCertFile),
		PrinterAuthority:   caDER,
		Expires:            now.Add(leafLifetime).UTC().Format(time.RFC3339),
	}, nil
}

// newAuthority makes the root a printer trusts. It is ECDSA because the
// firmware takes nothing else, and it signs leaves only (pathlen 0), as the
// printer's flat chain asks.
func newAuthority(now time.Time) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("make the authority's key: %w", err)
	}

	serial, err := serialNumber()
	if err != nil {
		return nil, nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Prusa Connect"},
			CommonName:   "Prusa Connect Root CA",
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign the authority: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read the authority back: %w", err)
	}

	return key, cert, der, nil
}

// newLeaf makes a server certificate for hosts, signed by the authority. An
// address is written both as an IP and as a DNS name, because the TLS library
// in the printer's firmware matches an address hostname against DNS names
// only.
func newLeaf(
	caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hosts []string, now time.Time,
) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("make a certificate's key: %w", err)
	}

	serial, err := serialNumber()
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Prusa Connect"}, CommonName: hosts[0]},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(leafLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		}

		template.DNSNames = append(template.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign a certificate: %w", err)
	}

	keyBytes, err := keyPEM(key)
	if err != nil {
		return nil, nil, err
	}

	return pemOf("CERTIFICATE", der), keyBytes, nil
}

// serialBits is how long a serial number is: under the 20 bytes RFC 5280
// allows, and random enough that two never meet.
const serialBits = 127

func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), serialBits)

	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("draw a serial number: %w", err)
	}

	return serial, nil
}

func keyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode a key: %w", err)
	}

	return pemOf("PRIVATE KEY", der), nil
}

func pemOf(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

// suggestedNames are the names a certificate is likely to need on this
// machine: its host name, the .local name mDNS announces, and its addresses
// on the local network.
func suggestedNames() []string {
	var names []string

	if host, err := os.Hostname(); err == nil && host != "" {
		short, _, _ := strings.Cut(strings.ToLower(host), ".")
		names = append(names, short, short+".local")
	}

	for _, ip := range localAddresses() {
		if !slices.Contains(names, ip) {
			names = append(names, ip)
		}
	}

	return names
}

// localAddresses are this machine's IPv4 addresses on the networks it is up
// on, without loopback, link-local addresses, and the network address a
// tunnel or a bridge can report, which nothing reaches this machine at.
func localAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []string

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addresses, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}

		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok && reachable(network) {
				out = append(out, network.IP.String())
			}
		}
	}

	return out
}

// reachable says whether an interface address is one a browser or a printer
// can dial.
func reachable(network *net.IPNet) bool {
	return network.IP.To4() != nil && !network.IP.IsLinkLocalUnicast() &&
		!network.IP.Equal(network.IP.Mask(network.Mask))
}

// errNotECDSA reports a printer certificate the firmware would refuse.
var errNotECDSA = errors.New("the printer's certificate has to use an ECDSA key")

// checkPrinterCertificate refuses a certificate the printer's firmware would
// refuse, while the person can still pick another one.
func checkPrinterCertificate(certificate string) error {
	raw, err := os.ReadFile(certificate)
	if err != nil {
		return fmt.Errorf("read %s: %w", certificate, err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return fmt.Errorf("%s holds no PEM certificate", certificate)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("read the certificate in %s: %w", certificate, err)
	}

	if _, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok {
		return errNotECDSA
	}

	return nil
}

// printerConnector is the connector instance the wizard writes the printers'
// listener under: the name and the type the server declares by itself when no
// connector is written, so the wizard changes the listener and nothing else.
func printerConnector() string { return string(builtin.ShippingConnector()) }

// printersBlock is the connector block that gives printers a listener of their
// own, checked the way the driver checks it at start, so a listener that would
// stop the start is refused while the person can still correct it.
func printersBlock(printers *httpapi.SetupPrinters, api string) (map[string]any, error) {
	addr := strings.TrimSpace(printers.Addr)

	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" || strings.Trim(port, "0123456789") != "" {
		return nil, httpfault.RequestInvalid("body.printers.addr", "a host and a port, such as :8443", addr)
	}

	if config.SameSocket(addr, api) {
		return nil, httpfault.RequestInvalid("body.printers.addr",
			"a port other than the API's", "the web app and the API already listen on "+api)
	}

	listener := map[string]any{"addr": net.JoinHostPort(host, port)}

	certificate, key := strings.TrimSpace(printers.Certificate), strings.TrimSpace(printers.Key)

	switch {
	case certificate == "" && key == "":
	case certificate == "" || key == "":
		return nil, httpfault.RequestInvalid("body.printers", "a certificate and its key, or neither", "")
	default:
		if _, err = tls.LoadX509KeyPair(certificate, key); err != nil {
			return nil, httpfault.RequestInvalid(
				"body.printers.certificate",
				"a certificate and its key in PEM",
				err.Error(),
			)
		}

		if err = checkPrinterCertificate(certificate); err != nil {
			return nil, httpfault.RequestInvalid("body.printers.certificate", "an ECDSA certificate", err.Error())
		}

		listener["tls"] = map[string]any{"certificate": certificate, "key": key}
	}

	return map[string]any{
		printerConnector(): map[string]any{
			"type":   printerConnector(),
			"config": map[string]any{"listener": listener},
		},
	}, nil
}

// printerAuthority reads the authority certificate the wizard wrote, on every
// request, so a certificate made after start is served. A deployment whose
// printers do not trust its own authority has none to give.
func printerAuthority(frontend config.Frontend, configPath string) func() ([]byte, error) {
	return func() ([]byte, error) {
		if !frontend.PrinterCustomCA {
			return nil, fs.ErrNotExist
		}

		return os.ReadFile(filepath.Join(certsPath(configPath), printerCAFile))
	}
}
