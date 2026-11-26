package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// setupConfigurationPath is the configuration contract the first-run wizard
// draws its fields from.
const setupConfigurationPath = firstRunPath + "/configuration"

// SetupKey is one configuration key as the first-run wizard draws it.
type SetupKey struct {
	Path     string   `json:"path"               doc:"The dotted path an operator writes in the file."                                                 example:"core.api.addr"`
	Group    string   `json:"group"              doc:"The part of the server the key belongs to. The wizard draws one section per group."              example:"api"`
	Level    string   `json:"level"              doc:"basic keys are asked on both paths of the wizard, advanced keys only on the advanced path."                              enum:"basic,advanced"`
	Kind     string   `json:"kind"               doc:"What a value looks like. A duration is written like 30s or 6h."                                                          enum:"text,switch,number,duration,text_list"`
	Default  any      `json:"default"            doc:"The value the server uses when nothing sets the key. Null for a key with no default."`
	Choices  []string `json:"choices"            doc:"The closed set of values the key takes. Empty for a key that takes any value of its kind."`
	Doc      string   `json:"doc"                doc:"The sentence the configuration reference prints for the key."`
	Required string   `json:"required,omitempty" doc:"When a deployment has to set the key. Absent when the default works."`
	Secret   bool     `json:"secret"             doc:"Whether the value is a secret. A secret is never shown back."`
	Locked   bool     `json:"locked"             doc:"Whether the operator's own file sets the key. Their value wins over anything the wizard writes."`
	Value    any      `json:"value,omitempty"    doc:"The value the operator's file sets, when the key is locked and not secret."`
	// Reference and Guide are pages of the documentation, relative to its
	// address: the key's own row, and the page about its part.
	Reference string `json:"reference"       doc:"The key's row on the configuration reference, relative to the documentation's address, such as config/network/#key-core-api-addr."`
	Guide     string `json:"guide,omitempty" doc:"The page that explains the part this key belongs to, relative to the documentation's address, such as certificates/. Absent when the reference is all there is."`
	// DependsOn is the key that turns this one on.
	DependsOn string `json:"depends_on,omitempty" doc:"The key that turns this one on: a switch written as its path, a key with a closed set written as path=value, or a text key written as its path, which turns this one on while it is not empty. Absent for a key that is always on." example:"core.discovery.enabled"`
}

// SetupFile is one of the two files a configured server reads.
type SetupFile struct {
	Path   string `json:"path"   doc:"The absolute path of the file." example:"/srv/prusa-connect/config/config.yaml"`
	Exists bool   `json:"exists" doc:"Whether the file is there."`
}

// SetupStorage is where the files a person uploads are kept.
type SetupStorage struct {
	Root     string `json:"root"     doc:"The folder the files are kept in."                                                      example:"/srv/prusa-connect/storage"`
	Declared bool   `json:"declared" doc:"Whether the operator's file already declares a storage. The wizard then asks for none."`
}

// SetupConfiguration is the configuration contract the wizard reads.
type SetupConfiguration struct {
	ConfigFile SetupFile    `json:"config_file" doc:"The operator's file. A key it sets wins over the wizard."`
	SetupFile  SetupFile    `json:"setup_file"  doc:"The file the wizard writes."`
	Keys       []SetupKey   `json:"keys"        doc:"Every key the wizard may ask, in the order the reference page lists them."`
	Storage    SetupStorage `json:"storage"     doc:"Where the files a person uploads are kept."`
	Hosts      []string     `json:"hosts"       doc:"The names and addresses this machine answers on, which a certificate the wizard makes is likely to need."`
	// Printers is whether the wizard may give printers a listener of their
	// own. False when the operator's file declares the connectors itself.
	Printers bool `json:"printers" doc:"Whether the wizard may give printers a listener of their own. False when the operator's file declares the connectors."`
	// PrintersTLS is whether the operator's file gives the printers' listener
	// a certificate, which is what the wizard shows when Printers is false.
	PrintersTLS bool `json:"printers_tls" doc:"Whether the operator's file gives the printers' listener a certificate. The wizard shows it in place of its own choice when printers is false."`
}

// SetupFailure is the start that failed after the last wizard was confirmed.
type SetupFailure struct {
	Message string `json:"message" doc:"What stopped the start, as the server logged it."`
	At      string `json:"at"      doc:"When the start failed, in RFC 3339."              format:"date-time"`
}

type setupConfigurationInput struct {
	Code string `header:"X-Setup-Code" maxLength:"128" doc:"The one-time setup code this server printed in its log when it started, or the value of core.auth.setup_code."`
}

type setupConfigurationOutput struct {
	Body SetupConfiguration
}

func registerSetupConfiguration(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "get-setup-configuration",
		Method:      http.MethodGet,
		Path:        setupConfigurationPath,
		Summary:     "The configuration the first-run wizard asks for",
		Description: "Lists every configuration key the first-run wizard may ask, built from the same " +
			"structs, sentences and defaults as the configuration reference. A key whose part this binary " +
			"was built without is left out, and so are the keys that decide where the server writes its " +
			"own files. A key the operator's file sets is locked, with its value unless it is a secret.\n\n" +
			"It takes the one-time setup code in `X-Setup-Code`, so a page learns at once whether the code " +
			"is right, and a wrong code counts against the address as it does on `record-setup`. It answers " +
			"409 once the first run is done.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors: []int{
			http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, in *setupConfigurationInput) (*setupConfigurationOutput, error) {
		contract, err := cfg.Setup.Configuration(ctx, setupClientOf(cfg, requestFrom(ctx)), in.Code)
		if err != nil {
			return nil, withRetryAfter(serverFault(err, "read the configuration the first run asks for"))
		}

		if contract.Keys == nil {
			contract.Keys = []SetupKey{}
		}

		if contract.Hosts == nil {
			contract.Hosts = []string{}
		}

		for index := range contract.Keys {
			if contract.Keys[index].Choices == nil {
				contract.Keys[index].Choices = []string{}
			}
		}

		return &setupConfigurationOutput{Body: contract}, nil
	})
}

// setupStoragePath checks the folder the wizard would keep uploads in.
const setupStoragePath = firstRunPath + "/storage"

// SetupStorageCheck is whether this process could keep uploads in a folder.
type SetupStorageCheck struct {
	Root     string `json:"root"              doc:"The folder, as an absolute path on this server."`
	Exists   bool   `json:"exists"            doc:"Whether the folder is there. A missing one is created when the wizard is confirmed."`
	Writable bool   `json:"writable"          doc:"Whether this process could write there: in the folder, or in the folder above it that the missing one would be created in."`
	Problem  string `json:"problem,omitempty" doc:"Why it could not, when it could not."                                                                                       enum:"not_a_folder,not_writable,no_parent"`
	Detail   string `json:"detail,omitempty"  doc:"What the operating system said, when it refused."`
}

type setupStorageInput struct {
	Code string `header:"X-Setup-Code" maxLength:"128" doc:"The one-time setup code this server printed in its log when it started, or the value of core.auth.setup_code."`
	Body struct {
		Root string `json:"root" doc:"The folder to check. A relative path is read from the folder the server was started in." maxLength:"4096" minLength:"1"`
	}
}

type setupStorageOutput struct {
	Body SetupStorageCheck
}

func registerSetupStorage(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "check-setup-storage",
		Method:      http.MethodPost,
		Path:        setupStoragePath,
		Summary:     "Whether the first run could keep uploads in a folder",
		Description: "Checks a folder for the storage the first-run wizard declares, before the wizard is " +
			"confirmed. It creates nothing: it writes one file and removes it, in the folder or, for a " +
			"folder that is missing, in the nearest folder above it, which is where the confirm creates it. " +
			"It takes the one-time setup code in `X-Setup-Code` and answers 409 once the first run is done.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors: []int{
			http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, in *setupStorageInput) (*setupStorageOutput, error) {
		check, err := cfg.Setup.CheckStorage(ctx, setupClientOf(cfg, requestFrom(ctx)), in.Code, in.Body.Root)
		if err != nil {
			return nil, withRetryAfter(serverFault(err, "check the folder for uploads"))
		}

		return &setupStorageOutput{Body: check}, nil
	})
}

// setupCertificatesPath makes the certificates the wizard offers.
const setupCertificatesPath = firstRunPath + "/certificates"

// SetupCertificates are the files the wizard made.
type SetupCertificates struct {
	Folder             string   `json:"folder"              doc:"The folder the files are in."`
	Names              []string `json:"names"               doc:"The names and addresses both certificates are valid for."`
	WebCertificate     string   `json:"web_certificate"     doc:"The certificate for the web app and the API, for core.api.tls.certificate."`
	WebKey             string   `json:"web_key"             doc:"Its key, for core.api.tls.key."`
	PrinterCertificate string   `json:"printer_certificate" doc:"The ECDSA certificate for the printers' listener."`
	PrinterKey         string   `json:"printer_key"         doc:"Its key."`
	Authority          string   `json:"authority"           doc:"The authority that signed both, in PEM, to trust in a browser or an operating system."`
	PrinterAuthority   []byte   `json:"printer_authority"   doc:"The same authority in DER, the file a printer loads from USB as connect.der."`
	Expires            string   `json:"expires"             doc:"When both certificates expire, in RFC 3339."                                           format:"date-time"`
}

type setupCertificatesInput struct {
	Code string `header:"X-Setup-Code" maxLength:"128" doc:"The one-time setup code this server printed in its log when it started, or the value of core.auth.setup_code."`
	Body struct {
		Names []string `json:"names" doc:"The names and addresses the certificates are valid for: the host names and the addresses printers and browsers reach this server at." maxItems:"32" minItems:"1"`
	}
}

type setupCertificatesOutput struct {
	Body SetupCertificates
}

func registerSetupCertificates(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "create-setup-certificates",
		Method:      http.MethodPost,
		Path:        setupCertificatesPath,
		Summary:     "Make the certificates the first run encrypts with",
		Description: "Makes an authority and two certificates it signs, one for the web app and one for the " +
			"printers' listener, valid for the names in the body, and writes them under config.d/certs. All " +
			"three use ECDSA P-256, which is the one key type a printer takes. A second call replaces the " +
			"files. The answer carries the authority in DER, which a printer loads from USB as connect.der.\n\n" +
			"It takes the one-time setup code in `X-Setup-Code` and answers 409 once the first run is done.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors: []int{
			http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity,
			http.StatusTooManyRequests, http.StatusInternalServerError,
		},
	}, func(ctx context.Context, in *setupCertificatesInput) (*setupCertificatesOutput, error) {
		made, err := cfg.Setup.CreateCertificates(ctx, setupClientOf(cfg, requestFrom(ctx)), in.Code, in.Body.Names)
		if err != nil {
			return nil, withRetryAfter(serverFault(err, "make the certificates"))
		}

		return &setupCertificatesOutput{Body: made}, nil
	})
}
