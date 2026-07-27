module github.com/ngicks/gahaku

go 1.26.4

require (
	github.com/aws/aws-sdk-go-v2 v1.43.0
	github.com/aws/aws-sdk-go-v2/credentials v1.19.30
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.0
	github.com/caarlos0/env/v11 v11.4.1
	github.com/gabriel-vasile/mimetype v1.4.15
	github.com/gen2brain/avif v0.6.0
	github.com/gen2brain/webp v0.6.4
	github.com/klippa-app/go-pdfium v1.19.5
	github.com/ngicks/go-common/contextkey v0.3.0
	github.com/spf13/cobra v1.10.2
	github.com/tetratelabs/wazero v1.12.0
	golang.org/x/image v0.44.0
	golang.org/x/sync v0.22.0
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.14 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.32 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.32 // indirect
	github.com/aws/smithy-go v1.27.3 // indirect
	github.com/ebitengine/purego v0.10.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jolestar/go-commons-pool/v2 v2.1.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)

// apm_modules is the APM CLI's dependency directory; its skill helper
// packages are not part of this module and would otherwise pull their own
// requirements into go.mod.
ignore ./apm_modules
