package json

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	_ "embed"

	"github.com/itchyny/gojq"
	"github.com/xeipuuv/gojsonschema"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/instill-ai/pipeline-backend/pkg/component/base"
	"github.com/instill-ai/x/errmsg"
)

const (
	taskMarshal      = "TASK_MARSHAL"
	taskUnmarshal    = "TASK_UNMARSHAL"
	taskJQ           = "TASK_JQ"
	taskRenameFields = "TASK_RENAME_FIELDS"
)

var (
	//go:embed config/definition.json
	definitionJSON []byte
	//go:embed config/tasks.json
	tasksJSON []byte
	//go:embed config/schema.json
	schemaJSON []byte

	once   sync.Once
	comp   *component
	schema *gojsonschema.Schema
)

type component struct {
	base.Component
}

type execution struct {
	base.ComponentExecution

	execute func(*structpb.Struct) (*structpb.Struct, error)
}

// Init initializes the JSON schema and returns a component instance.
func Init(bc base.Component) *component {
	once.Do(func() {
		comp = &component{Component: bc}
		err := comp.LoadDefinition(definitionJSON, nil, tasksJSON, nil)
		if err != nil {
			panic(err)
		}

		// Load the JSON schema
		schemaLoader := gojsonschema.NewStringLoader(string(schemaJSON))
		schema, err = gojsonschema.NewSchema(schemaLoader)
		if err != nil {
			panic(fmt.Sprintf("Failed to load JSON schema: %v", err))
		}
	})
	return comp
}

// CreateExecution initializes a component executor that can be used in a pipeline trigger.
func (c *component) CreateExecution(x base.ComponentExecution) (base.IExecution, error) {
	e := &execution{ComponentExecution: x}

	switch x.Task {
	case taskMarshal:
		e.execute = e.marshal
	case taskUnmarshal:
		e.execute = e.unmarshal
	case taskJQ:
		e.execute = e.jq
	case taskRenameFields:
		e.execute = e.renameFields
	default:
		return nil, errmsg.AddMessage(
			fmt.Errorf("not supported task: %s", x.Task),
			fmt.Sprintf("%s task is not supported.", x.Task),
		)
	}
	return e, nil
}

// validateJSON validates input JSON against the schema.
func validateJSON(input any) error {
	documentLoader := gojsonschema.NewGoLoader(input)
	result, err := schema.Validate(documentLoader)
	if err != nil {
		return fmt.Errorf("validation error: %v", err)
	}
	if !result.Valid() {
		errMsg := "JSON does not conform to the schema: "
		for _, desc := range result.Errors() {
			errMsg += fmt.Sprintf("%s; ", desc)
		}
		return fmt.Errorf(errMsg)
	}
	return nil
}

func (e *execution) marshal(in *structpb.Struct) (*structpb.Struct, error) {
	out := new(structpb.Struct)

	input := in.AsMap()
	if err := validateJSON(input); err != nil {
		return nil, errmsg.AddMessage(err, "Validation failed for marshal task.")
	}

	b, err := protojson.Marshal(in.Fields["json"])
	if err != nil {
		return nil, errmsg.AddMessage(err, "Couldn't convert the provided object to JSON.")
	}

	out.Fields = map[string]*structpb.Value{
		"string": structpb.NewStringValue(string(b)),
	}

	return out, nil
}

func (e *execution) unmarshal(in *structpb.Struct) (*structpb.Struct, error) {
	out := new(structpb.Struct)

	b := []byte(in.Fields["string"].GetStringValue())
	obj := new(structpb.Value)
	if err := protojson.Unmarshal(b, obj); err != nil {
		return nil, errmsg.AddMessage(err, "Couldn't parse the JSON string. Please check the syntax is correct.")
	}

	if err := validateJSON(obj.AsInterface()); err != nil {
		return nil, errmsg.AddMessage(err, "Validation failed for unmarshal task.")
	}

	out.Fields = map[string]*structpb.Value{"json": obj}

	return out, nil
}

func (e *execution) jq(in *structpb.Struct) (*structpb.Struct, error) {
	out := new(structpb.Struct)

	input := in.Fields["json-value"].AsInterface()
	if input == nil {
		b := []byte(in.Fields["json-string"].GetStringValue())
		if err := json.Unmarshal(b, &input); err != nil {
			return nil, errmsg.AddMessage(err, "Couldn't parse the JSON input. Please check the syntax is correct.")
		}
	}

	if err := validateJSON(input); err != nil {
		return nil, errmsg.AddMessage(err, "Validation failed for jq task.")
	}

	queryStr := in.Fields["jq-filter"].GetStringValue()
	q, err := gojq.Parse(queryStr)
	if err != nil {
		msg := fmt.Sprintf("Couldn't parse the jq filter: %s. Please check the syntax is correct.", err.Error())
		return nil, errmsg.AddMessage(err, msg)
	}

	results := []any{}
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}

		if err, ok := v.(error); ok {
			msg := fmt.Sprintf("Couldn't apply the jq filter: %s.", err.Error())
			return nil, errmsg.AddMessage(err, msg)
		}

		results = append(results, v)
	}

	list, err := structpb.NewList(results)
	if err != nil {
		return nil, err
	}

	out.Fields = map[string]*structpb.Value{
		"results": structpb.NewListValue(list),
	}

	return out, nil
}

// renameFields renames fields in a JSON object according to the provided mapping.
func (e *execution) renameFields(in *structpb.Struct) (*structpb.Struct, error) {
	out := new(structpb.Struct)

	jsonValue := in.Fields["json"].AsInterface()
	fields := in.Fields["fields"].GetListValue().Values
	conflictResolution := in.Fields["conflict-resolution"].GetStringValue()

	// Check for the required fields
	if jsonValue == nil || len(fields) == 0 {
		return nil, errmsg.AddMessage(fmt.Errorf("missing required fields: json and fields"), "JSON and fields are required.")
	}

	// Perform renaming
	for _, field := range fields {
		from := field.GetStructValue().Fields["from"].GetStringValue()
		to := field.GetStructValue().Fields["to"].GetStringValue()

		if val, ok := jsonValue.(map[string]interface{})[from]; ok {
			switch conflictResolution {
			case "overwrite":
				delete(jsonValue.(map[string]interface{}), from)
				jsonValue.(map[string]interface{})[to] = val
			case "skip":
				if _, exists := jsonValue.(map[string]interface{})[to]; !exists {
					delete(jsonValue.(map[string]interface{}), from)
					jsonValue.(map[string]interface{})[to] = val
				}
			case "error":
				if _, exists := jsonValue.(map[string]interface{})[to]; exists {
					return nil, errmsg.AddMessage(fmt.Errorf("field conflict: '%s' already exists", to), "Field conflict.")
				}
				delete(jsonValue.(map[string]interface{}), from)
				jsonValue.(map[string]interface{})[to] = val
			default:
				return nil, errmsg.AddMessage(fmt.Errorf("invalid conflict resolution strategy"), "Conflict resolution strategy is invalid.")
			}
		}
	}

	// Validate the output JSON against the schema
	if err := validateJSON(jsonValue); err != nil {
		return nil, errmsg.AddMessage(err, "Validation failed for renamed JSON object.")
	}

	out.Fields = map[string]*structpb.Value{
		"json": structpb.NewStructValue(structpb.NewStruct(jsonValue.(map[string]interface{}))),
	}

	return out, nil
}

func (e *execution) Execute(ctx context.Context, jobs []*base.Job) error {
	return base.SequentialExecutor(ctx, jobs, e.execute)
}
