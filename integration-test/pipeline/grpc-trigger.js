import grpc from "k6/net/grpc";

import { check, group } from "k6";
import { randomString } from "https://jslib.k6.io/k6-utils/1.1.0/index.js";

import * as constant from "./const.js";

const client = new grpc.Client();
client.load(["../proto/vdp/pipeline/v1alpha"], "pipeline_public_service.proto");

export function CheckTrigger(metadata) {
  group(
    "Pipelines API: Trigger a pipeline for single image and single model",
    () => {
      client.connect(constant.pipelineGRPCPublicHost, {
        plaintext: true,
      });

      var reqGRPC = Object.assign(
        {
          id: randomString(10),
          description: randomString(50),
        },
        constant.simpleRecipe
      );

      check(
        client.invoke(
          "vdp.pipeline.v1alpha.PipelinePublicService/CreateUserPipeline",
          {
            parent: `${constant.namespace}`,
            pipeline: reqGRPC,
          },
          metadata
        ),
        {
          "vdp.pipeline.v1alpha.PipelinePublicService/CreateUserPipeline GRPC pipeline response StatusOK":
            (r) => r.status === grpc.StatusOK,
        }
      );

      check(
        client.invoke(
          "vdp.pipeline.v1alpha.PipelinePublicService/TriggerUserPipeline",
          {
            name: `${constant.namespace}/pipelines/${reqGRPC.id}`,
            inputs: constant.simplePayload.inputs,
          },
          metadata
        ),
        {
          [`vdp.pipeline.v1alpha.PipelinePublicService/TriggerUserPipeline (url) response StatusOK`]:
            (r) => r.status === grpc.StatusOK,
        }
      );


      check(
        client.invoke(
          `vdp.pipeline.v1alpha.PipelinePublicService/DeleteUserPipeline`,
          {
            name: `${constant.namespace}/pipelines/${reqGRPC.id}`,
          },
          metadata
        ),
        {
          [`vdp.pipeline.v1alpha.PipelinePublicService/DeleteUserPipeline ${reqGRPC.id} response StatusOK`]:
            (r) => r.status === grpc.StatusOK,
        }
      );



      client.close();
    }
  );
}