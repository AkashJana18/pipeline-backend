package constant

const (
	_  = iota
	KB = 1 << (10 * iota)
	MB
	GB
	TB
)

const MaxBatchSize int = 32
const MaxPayloadSize = 1024 * 1024 * 32

// Constants for resource owner
const DefaultUserID string = "admin"
const HeaderUserUIDKey = "jwt-sub"
const HeaderInstillCodeKey = "instill-share-code"
const StartConnectorId = "op-start"
const EndConnectorId = "op-end"
const ReturnTracesKey = "instill-return-traces"
