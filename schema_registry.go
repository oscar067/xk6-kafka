package kafka

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/riferrei/srclient"
)

type Element string

const (
	Key                Element = "key"
	Value              Element = "value"
	MagicPrefixSize    int     = 5
	ConcurrentRequests int     = 16
)

type BasicAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type SchemaRegistryConfiguration struct {
	URL       string    `json:"url"`
	BasicAuth BasicAuth `json:"basicAuth"`
	UseLatest bool      `json:"useLatest"`
	TLS       TLSConfig `json:"tls"`
}

const (
	TopicNameStrategy       string = "TopicNameStrategy"
	RecordNameStrategy      string = "RecordNameStrategy"
	TopicRecordNameStrategy string = "TopicRecordNameStrategy"
)

// DecodeWireFormat removes the proprietary 5-byte prefix from the Avro, ProtoBuf
// or JSONSchema payload.
// https://docs.confluent.io/platform/current/schema-registry/serdes-develop/index.html#wire-format
func DecodeWireFormat(message []byte) (int, []byte, *Xk6KafkaError) {
	if len(message) < MagicPrefixSize {
		return 0, nil, NewXk6KafkaError(messageTooShort,
			"Invalid message: message too short to contain schema id.", nil)
	}
	if message[0] != 0 {
		return 0, nil, NewXk6KafkaError(messageTooShort,
			"Invalid message: invalid start byte.", nil)
	}
	magicPrefix := int(binary.BigEndian.Uint32(message[1:MagicPrefixSize]))
	return magicPrefix, message[MagicPrefixSize:], nil
}

// EncodeWireFormat adds the proprietary 5-byte prefix to the Avro, ProtoBuf or
// JSONSchema payload.
// https://docs.confluent.io/platform/current/schema-registry/serdes-develop/index.html#wire-format
func EncodeWireFormat(data []byte, schemaID int) []byte {
	schemaIDBytes := make([]byte, MagicPrefixSize-1)
	binary.BigEndian.PutUint32(schemaIDBytes, uint32(schemaID))
	return append(append([]byte{0}, schemaIDBytes...), data...)
}

// SchemaRegistryClientWithConfiguration creates a SchemaRegistryClient instance
// with the given configuration. It will also configure auth and TLS credentials if exists.
func SchemaRegistryClientWithConfiguration(configuration SchemaRegistryConfiguration) *srclient.SchemaRegistryClient {
	var srClient *srclient.SchemaRegistryClient

	tlsConfig, err := GetTLSConfig(configuration.TLS)
	if err != nil {
		// Ignore the error if we're not using TLS
		if err.Code != noTLSConfig {
			logger.WithField("error", err).Error("Cannot process TLS config")
		}
		srClient = srclient.CreateSchemaRegistryClient(configuration.URL)
	}

	if tlsConfig != nil {
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		}
		srClient = srclient.CreateSchemaRegistryClientWithOptions(
			configuration.URL, httpClient, ConcurrentRequests)
	}

	if configuration.BasicAuth.Username != "" && configuration.BasicAuth.Password != "" {
		srClient.SetCredentials(configuration.BasicAuth.Username, configuration.BasicAuth.Password)
	}

	return srClient
}

var cache = make(map[string]*srclient.Schema)

// GetSchema returns the schema for the given subject and schema ID and version.
func GetSchema(
	client *srclient.SchemaRegistryClient, subject string, schema string, schemaType srclient.SchemaType, version int,
) (*srclient.Schema, *Xk6KafkaError) {
	// The client always caches the schema.
	var schemaInfo *srclient.Schema
	var err error
	// Default version of the schema is the latest version.

	if value, exists := cache[subject]; exists {
		return value, nil
	}

	if version == 0 {
		schemaInfo, err = client.GetLatestSchema(subject)
	} else {
		schemaInfo, err = client.GetSchemaByVersion(subject, version)
	}

	if err == nil {
		cache[subject] = schemaInfo
	} else {
		return nil, NewXk6KafkaError(schemaNotFound,
			"Failed to get schema from schema registry", err)
	}

	return schemaInfo, nil
}

// CreateSchema creates a new schema in the schema registry.
func CreateSchema(
	client *srclient.SchemaRegistryClient, subject string, schema string, schemaType srclient.SchemaType,
) (*srclient.Schema, *Xk6KafkaError) {
	schemaInfo, err := client.CreateSchema(subject, schema, schemaType)
	if err != nil {
		return nil, NewXk6KafkaError(schemaCreationFailed, "Failed to create schema.", err)
	}
	return schemaInfo, nil
}

// GetSubjectName return the subject name strategy for the given schema and topic.
func GetSubjectName(schema string, topic string, element Element, subjectNameStrategy string) (string, *Xk6KafkaError) {
	if subjectNameStrategy == "" || subjectNameStrategy == TopicNameStrategy {
		return topic + "-" + string(element), nil
	}

	var schemaMap map[string]interface{}
	err := json.Unmarshal([]byte(schema), &schemaMap)
	if err != nil {
		return "", NewXk6KafkaError(failedToUnmarshalSchema, "Failed to unmarshal schema", nil)
	}
	recordName := ""
	if namespace, ok := schemaMap["namespace"]; ok {
		if namespace, ok := namespace.(string); ok {
			recordName = namespace + "."
		} else {
			return "", NewXk6KafkaError(failedTypeCast, "Failed to cast to string", nil)
		}
	}
	if name, ok := schemaMap["name"]; ok {
		if name, ok := name.(string); ok {
			recordName += name
		} else {
			return "", NewXk6KafkaError(failedTypeCast, "Failed to cast to string", nil)
		}
	}

	if subjectNameStrategy == RecordNameStrategy {
		return recordName, nil
	}
	if subjectNameStrategy == TopicRecordNameStrategy {
		return topic + "-" + recordName, nil
	}

	return "", NewXk6KafkaError(failedEncodeToAvro, fmt.Sprintf(
		"Unknown subject name strategy: %v", subjectNameStrategy), nil)
}
