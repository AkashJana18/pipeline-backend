package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/instill-ai/pipeline-backend/config"
	"github.com/instill-ai/pipeline-backend/pkg/datamodel"
	"github.com/instill-ai/pipeline-backend/pkg/logger"
	"github.com/instill-ai/pipeline-backend/pkg/utils"

	mgmtPB "github.com/instill-ai/protogen-go/base/mgmt/v1alpha"
	connectorPB "github.com/instill-ai/protogen-go/vdp/connector/v1alpha"
)

type TriggerAsyncPipelineWorkflowRequest struct {
	PipelineInputBlobRedisKeys []string
	Pipeline                   *datamodel.Pipeline
}

// ExecuteConnectorActivityRequest represents the parameters for TriggerActivity
type ExecuteConnectorActivityRequest struct {
	InputBlobRedisKeys []string
	Name               string
	OwnerPermalink     string
	PipelineMetadata   PipelineMetadataStruct
}

type PipelineMetadataStruct struct {
	Id        string
	Uid       string
	Owner     string
	TriggerId string
}

type ExecuteConnectorActivityResponse struct {
	OutputBlobRedisKeys []string
}

var tracer = otel.Tracer("pipeline-backend.temporal.tracer")

func (w *worker) GetBlob(redisKeys []string) ([]*structpb.Struct, error) {
	payloads := []*structpb.Struct{}
	for idx := range redisKeys {
		blob, err := w.redisClient.Get(context.Background(), redisKeys[idx]).Bytes()
		if err != nil {
			return nil, err
		}
		payload := &structpb.Struct{}
		err = protojson.Unmarshal(blob, payload)
		if err != nil {
			return nil, err
		}

		payloads = append(payloads, payload)

	}
	return payloads, nil
}

func (w *worker) SetBlob(inputs []*structpb.Struct) ([]string, error) {
	id, _ := uuid.NewV4()
	blobRedisKeys := []string{}
	for idx, input := range inputs {
		inputJson, err := protojson.Marshal(input)
		if err != nil {
			return nil, err
		}

		blobRedisKey := fmt.Sprintf("async_connector_blob:%s:%d", id.String(), idx)
		w.redisClient.Set(
			context.Background(),
			blobRedisKey,
			inputJson,
			time.Duration(config.Config.Server.Workflow.MaxWorkflowTimeout)*time.Second,
		)
		blobRedisKeys = append(blobRedisKeys, blobRedisKey)
	}
	return blobRedisKeys, nil
}

// TriggerAsyncPipelineWorkflow is a pipeline trigger workflow definition.
func (w *worker) TriggerAsyncPipelineWorkflow(ctx workflow.Context, param *TriggerAsyncPipelineWorkflowRequest) error {

	startTime := time.Now()
	eventName := "TriggerAsyncPipelineWorkflow"

	sCtx, span := tracer.Start(context.Background(), eventName,
		trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	logger, _ := logger.GetZapLogger(sCtx)
	logger.Info("TriggerAsyncPipelineWorkflow started")

	dataPoint := utils.UsageMetricData{
		OwnerUID:           strings.Split(param.Pipeline.Owner, "/")[1],
		TriggerMode:        mgmtPB.Mode_MODE_ASYNC,
		PipelineID:         param.Pipeline.ID,
		PipelineUID:        param.Pipeline.UID.String(),
		PipelineTriggerUID: workflow.GetInfo(ctx).WorkflowExecution.ID,
		TriggerTime:        startTime.Format(time.RFC3339Nano),
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: time.Duration(config.Config.Server.Workflow.MaxWorkflowTimeout) * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: config.Config.Server.Workflow.MaxActivityRetry,
		},
	}

	// TODO: parallel
	dag, err := utils.GenerateDAG(param.Pipeline.Recipe.Components)
	if err != nil {
		span.SetStatus(1, err.Error())
		dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
		dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
		_ = w.writeNewDataPoint(sCtx, dataPoint)
		return err
	}

	orderedComp, err := dag.TopoloicalSort()
	if err != nil {
		span.SetStatus(1, err.Error())
		dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
		dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
		_ = w.writeNewDataPoint(sCtx, dataPoint)
		return err
	}

	result := ExecuteConnectorActivityResponse{}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var inputs [][]byte
	pipelineInputs, err := w.GetBlob(param.PipelineInputBlobRedisKeys)
	if err != nil {
		span.SetStatus(1, err.Error())
		dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
		dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
		_ = w.writeNewDataPoint(sCtx, dataPoint)
		return err
	}
	batchSize := len(pipelineInputs)
	for idx := range pipelineInputs {
		inputStruct := &structpb.Struct{
			Fields: map[string]*structpb.Value{},
		}
		inputStruct.Fields["body"] = structpb.NewStructValue(pipelineInputs[idx])

		input, err := protojson.Marshal(inputStruct)
		if err != nil {
			span.SetStatus(1, err.Error())
			dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
			dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
			_ = w.writeNewDataPoint(sCtx, dataPoint)
			return err
		}
		inputs = append(inputs, input)
	}

	cache := make([]map[string]interface{}, batchSize)

	for idx := range inputs {
		cache[idx] = map[string]interface{}{}
		var inputStruct map[string]interface{}
		err := json.Unmarshal(inputs[idx], &inputStruct)
		if err != nil {
			span.SetStatus(1, err.Error())
			dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
			dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
			_ = w.writeNewDataPoint(sCtx, dataPoint)
			return err
		}
		cache[idx][orderedComp[0].Id] = inputStruct
	}

	responseCompId := ""

	for _, comp := range orderedComp[1:] {
		var compInputs []*structpb.Struct
		for idx := 0; idx < batchSize; idx++ {
			compInputTemplate := comp.Configuration
			compInputTemplateJson, err := protojson.Marshal(compInputTemplate)
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}

			var compInputTemplateStruct interface{}
			err = json.Unmarshal(compInputTemplateJson, &compInputTemplateStruct)
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}

			compInputStruct, err := utils.RenderInput(compInputTemplateStruct, cache[idx])
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}
			compInputJson, err := json.Marshal(compInputStruct)
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}

			compInput := &structpb.Struct{}
			err = protojson.Unmarshal([]byte(compInputJson), compInput)
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}

			compInputs = append(compInputs, compInput)
		}

		if comp.ResourceName != "" {
			inputBlobRedisKeys, err := w.SetBlob(compInputs)
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}
			for idx := range result.OutputBlobRedisKeys {
				defer w.redisClient.Del(context.Background(), inputBlobRedisKeys[idx])
			}
			result := ExecuteConnectorActivityResponse{}
			ctx = workflow.WithActivityOptions(ctx, ao)
			if err := workflow.ExecuteActivity(ctx, w.ConnectorActivity, &ExecuteConnectorActivityRequest{
				InputBlobRedisKeys: inputBlobRedisKeys,
				Name:               comp.ResourceName,
				OwnerPermalink:     param.Pipeline.Owner,
				PipelineMetadata: PipelineMetadataStruct{
					Id:        param.Pipeline.ID,
					Uid:       param.Pipeline.BaseDynamic.UID.String(),
					Owner:     param.Pipeline.Owner,
					TriggerId: workflow.GetInfo(ctx).WorkflowExecution.ID,
				},
			}).Get(ctx, &result); err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}

			outputs, err := w.GetBlob(result.OutputBlobRedisKeys)
			for idx := range result.OutputBlobRedisKeys {
				defer w.redisClient.Del(context.Background(), result.OutputBlobRedisKeys[idx])
			}
			if err != nil {
				span.SetStatus(1, err.Error())
				dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
				dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
				_ = w.writeNewDataPoint(sCtx, dataPoint)
				return err
			}
			for idx := range outputs {

				outputJson, err := protojson.Marshal(outputs[idx])
				if err != nil {
					return err
				}
				var outputStruct map[string]interface{}
				err = json.Unmarshal(outputJson, &outputStruct)
				if err != nil {
					return err
				}
				cache[idx][comp.Id] = outputStruct
			}

		}

		if comp.DefinitionName == "operator-definitions/end-operator" {
			responseCompId = comp.Id
			for idx := range compInputs {
				outputJson, err := protojson.Marshal(compInputs[idx])
				if err != nil {
					return err
				}
				var outputStruct map[string]interface{}
				err = json.Unmarshal(outputJson, &outputStruct)
				if err != nil {
					return err
				}
				cache[idx][comp.Id] = outputStruct
			}

		}
	}

	pipelineOutputs := []*structpb.Struct{}
	if responseCompId == "" {
		for idx := 0; idx < batchSize; idx++ {
			pipelineOutputs = append(pipelineOutputs, &structpb.Struct{})
		}
	} else {
		for idx := 0; idx < batchSize; idx++ {
			pipelineOutputJson, err := json.Marshal(cache[idx][responseCompId].(map[string]interface{})["body"])
			if err != nil {
				return err
			}
			pipelineOutput := &structpb.Struct{}
			err = protojson.Unmarshal(pipelineOutputJson, pipelineOutput)
			if err != nil {
				return err
			}
			pipelineOutputs = append(pipelineOutputs, pipelineOutput)

		}
	}

	for idx := range pipelineOutputs {
		outputJson, err := protojson.Marshal(pipelineOutputs[idx])
		if err != nil {
			span.SetStatus(1, err.Error())
			dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
			dataPoint.Status = mgmtPB.Status_STATUS_ERRORED
			_ = w.writeNewDataPoint(sCtx, dataPoint)
			return err
		}

		blobRedisKey := fmt.Sprintf("async_pipeline_response:%s:%d", workflow.GetInfo(ctx).WorkflowExecution.ID, idx)
		w.redisClient.Set(
			context.Background(),
			blobRedisKey,
			outputJson,
			time.Duration(config.Config.Server.Workflow.MaxWorkflowTimeout)*time.Second,
		)
	}

	dataPoint.ComputeTimeDuration = time.Since(startTime).Seconds()
	dataPoint.Status = mgmtPB.Status_STATUS_COMPLETED
	if err := w.writeNewDataPoint(sCtx, dataPoint); err != nil {
		logger.Warn(err.Error())
	}
	logger.Info("TriggerAsyncPipelineWorkflow completed")
	return nil
}

func (w *worker) ConnectorActivity(ctx context.Context, param *ExecuteConnectorActivityRequest) (*ExecuteConnectorActivityResponse, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("ConnectorActivity started")

	inputs, err := w.GetBlob(param.InputBlobRedisKeys)
	if err != nil {
		return nil, err
	}

	resp, err := w.connectorPublicServiceClient.ExecuteConnectorResource(
		utils.InjectOwnerToContextWithOwnerPermalink(
			metadata.AppendToOutgoingContext(ctx,
				"id", param.PipelineMetadata.Id,
				"uid", param.PipelineMetadata.Uid,
				"owner", param.PipelineMetadata.Owner,
				"trigger_id", param.PipelineMetadata.TriggerId,
			),
			param.OwnerPermalink),
		&connectorPB.ExecuteConnectorResourceRequest{
			Name:   param.Name,
			Inputs: inputs,
		},
	)
	if err != nil {
		logger.Error(fmt.Sprintf("[connector-backend] Error %s at connector %s: %v", "ExecuteConnector", param.Name, err.Error()))
		return nil, err
	}

	outputBlobRedisKeys, err := w.SetBlob(resp.Outputs)
	if err != nil {
		return nil, err
	}

	logger.Info("ConnectorActivity completed")
	return &ExecuteConnectorActivityResponse{OutputBlobRedisKeys: outputBlobRedisKeys}, nil
}
