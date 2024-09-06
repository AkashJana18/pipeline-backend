import http from "k6/http";
import { check, group } from "k6";
import { randomString } from "https://jslib.k6.io/k6-utils/1.1.0/index.js";

import {
  pipelinePublicHost,
  defaultUsername,
  dbIDPrefix
} from "./const.js";

import { deepEqual } from "./helper.js";

const defaultPageSize = 10;

export function CheckIntegrations() {
  group("Integration API: Get integration", () => {
    // Inexistent component
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations/restapio`, null, null), {
      "GET /v1beta/integrations/restapio response status is 404": (r) => r.status === 404,
      "GET /v1beta/integrations/restapio response contains end-user message": (r) => r.json().message === "Integration does not exist.",
    });

    // Component without setup
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations/document`, null, null), {
      "GET /v1beta/integrations/document response status is 404": (r) => r.status === 404,
      "GET /v1beta/integrations/document response contains end-user message": (r) => r.json().message === "Integration does not exist.",
    });

    var id = "pinecone";
    var cdef = http.request("GET", `${pipelinePublicHost}/v1beta/connector-definitions/${id}`, null, null).
      json().connectorDefinition;

    var integration = {
      uid: cdef.uid,
      id: cdef.id,
      title: cdef.title,
      description: cdef.description,
      vendor: cdef.vendor,
      icon: cdef.icon,
      featured: false, // TODO when protogen-go is updated, this will be removed
      schemas: [],
      view: "VIEW_BASIC"
    };

    // Basic view
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations/${id}`, null, null), {
      [`GET /v1beta/integrations/${id} response status is 200`]: (r) => r.status === 200,
      [`GET /v1beta/integrations/${id} response contains expected integration`]: (r) => deepEqual(r.json().integration, integration),
    });

    // Full view
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations/${id}?view=VIEW_FULL`, null, null), {
      [`GET /v1beta/integrations/${id}?view=VIEW_FULL response status is 200`]: (r) => r.status === 200,
      [`GET /v1beta/integrations/${id}?view=VIEW_FULL response contains schema`]: (r) => r.json().integration.schemas[0].method === "METHOD_DICTIONARY",
    });
  });

  group("Integration API: List integrations", () => {
    // Default pagination.
    var firstPage = http.request("GET", `${pipelinePublicHost}/v1beta/integrations`, null, null);
    check(firstPage, {
      "GET /v1beta/integrations response status is 200": (r) => r.status === 200,
      "GET /v1beta/integrations response totalSize > 0": (r) => r.json().totalSize > 0,
      "GET /v1beta/integrations has default page size": (r) => r.json().integrations.length === defaultPageSize,
    });

    // Non-default pagination, non-first page
    var tokenPageTwo = firstPage.json().nextPageToken;
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations?pageSize=2&pageToken=${tokenPageTwo}`, null, null), {
      [`GET /v1beta/integrations?pageSize=2&pageToken=${tokenPageTwo} response status is 200`]: (r) => r.status === 200,
      [`GET /v1beta/integrations?pageSize=2&pageToken=${tokenPageTwo} has page size 2"`]: (r) => r.json().integrations.length === 2,
      [`GET /v1beta/integrations?pageSize=2&pageToken=${tokenPageTwo} has different elements than page 1"`]: (r) =>
        r.json().integrations[0].id != firstPage.json().integrations[0].id,
    });

    // Filter fuzzy title
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations?filter=qIntegration="que"`, null, null), {
      [`GET /v1beta/integrations?filter=qIntegration="que" response status is 200`]: (r) => r.status === 200,
      [`GET /v1beta/integrations?filter=qIntegration="que" response totalSize > 0`]: (r) => r.json().totalSize === 1,
      [`GET /v1beta/integrations?filter=qIntegration="que" returns BigQuery integration`]: (r) => r.json().integrations[0].title === "BigQuery",
    });

    // Filter fuzzy vendor
    check(http.request("GET", `${pipelinePublicHost}/v1beta/integrations?filter=qIntegration="labs"`, null, null), {
      [`GET /v1beta/integrations?filter=qIntegration="labs" response status is 200`]: (r) => r.status === 200,
      [`GET /v1beta/integrations?filter=qIntegration="labs" response totalSize > 0`]: (r) => r.json().totalSize === 1,
      [`GET /v1beta/integrations?filter=qIntegration="labs" returns Redis integration`]: (r) => r.json().integrations[0].title === "Redis",
      [`GET /v1beta/integrations?filter=qIntegration="labs" intgration vendor matches Redis Labs`]: (r) => r.json().integrations[0].vendor === "Redis Labs",
    });
  });
}

export function CheckConnections(data) {
  var connectionID = dbIDPrefix + randomString(8);
  var setup = Object.assign({
    "email-address": "wombat@instill.tech",
    password: "0123",
    "server-address": "localhost",
    "server-port": 993,
  });

  group("Integration API: Create connection", () => {
    var path = `/v1beta/namespaces/${defaultUsername}/connections`;

    // Successful creation
    var okReq = http.request(
      "POST",
      pipelinePublicHost + path,
      JSON.stringify({
        id: connectionID,
        integrationId: "email",
        method: "METHOD_DICTIONARY",
        setup: setup,
      }),
      data.header
    );
    check(okReq, {
      [`POST ${path} response status is 201`]: (r) => r.status === 201,
      [`POST ${path} has a UID`]: (r) => r.json().connection.uid.length > 0,
      [`POST ${path} has a creation time`]: (r) => new Date(r.json().connection.createTime).getTime() > new Date().setTime(0),
    });

    // Check ID format
    var invalidID = dbIDPrefix + "This-Is-Invalid";
    var invalidIDReq = http.request(
      "POST",
      pipelinePublicHost + path,
      JSON.stringify({
        id: invalidID,
        integrationId: "email",
        method: "METHOD_DICTIONARY",
        setup: {},
      }),
      data.header
    );
    check(invalidIDReq, {
      [`POST ${path} response status is 400 with ID ${invalidID}`]: (r) => r.status === 400,
    });

    var invalidSetupReq = http.request(
      "POST",
      pipelinePublicHost + path,
      JSON.stringify({
        id: dbIDPrefix + randomString(16),
        integrationId: "email",
        method: "METHOD_DICTIONARY",
        setup: {
          "email-address": "wombat@instill.tech",
          password: "0123",
          "server-address": "localhost",
          "server-port": "993", // Should be string
        },
      }),
      data.header
    );
    check(invalidIDReq, {
      [`POST ${path} response status is 400`]: (r) => r.status === 400,
    });
  });

  group("Integration API: Get connection", () => {
    var path = `/v1beta/namespaces/${defaultUsername}/connections/${connectionID}`;

    check(http.request("GET", pipelinePublicHost + path + "aaa", null, data.header), {
      [`GET ${path + "aaa"} response status is 404`]: (r) => r.status === 404,
    });

    // Basic view
    check(http.request("GET", pipelinePublicHost + path, null, data.header), {
      [`GET ${path} response status is 200`]: (r) => r.status === 200,
      [`GET ${path} has basic view`]: (r) => r.json().connection.view === "VIEW_BASIC",
      [`GET ${path} has setup hidden`]: (r) => r.json().connection.setup === null,
      [`GET ${path} has integration ID`]: (r) => r.json().connection.integrationId === "email",
      [`GET ${path} has integration title`]: (r) => r.json().connection.integrationTitle === "Email",
    });

    // Full view
    check(http.request("GET", pipelinePublicHost + path + "?view=VIEW_FULL", null, data.header), {
      [`GET ${path + "?view=VIEW_FULL"} response status is 200`]: (r) => r.status === 200,
      [`GET ${path + "?view=VIEW_FULL"} has full view`]: (r) => r.json().connection.view === "VIEW_FULL",
      [`GET ${path + "?view=VIEW_FULL"} has setup`]: (r) => r.json().connection.setup != null,
      [`GET ${path + "?view=VIEW_FULL"} has setup value`]: (r) => r.json().connection.setup.password === setup.password, // TODO: redact
    });
  });

  group("Integration API: List connections", () => {
    var path = `/v1beta/namespaces/${defaultUsername}/connections`;
    var nConnections = 12;
    var integrationID = "openai";

    for (var i = 0; i < nConnections; i++) {
      var req = http.request(
        "POST",
        pipelinePublicHost + path,
        JSON.stringify({
          id: dbIDPrefix + randomString(8),
          integrationId: integrationID,
          method: "METHOD_DICTIONARY",
          setup: {
            "api-key": randomString(16),
          },
        }),
        data.header
      );
      check(req, { [`POST ${path}[${i}] response status is 201`]: (r) => r.status === 201 });
    }


    // With connection ID filter
    var pathWithFilter =  path + `?filter=qConnection="${dbIDPrefix}"`;
    var firstPage = http.request("GET", pipelinePublicHost + pathWithFilter, null, data.header);
    check(firstPage, {
      [`GET ${pathWithFilter} response status is 200`]: (r) => r.status === 200,
      [`GET ${pathWithFilter} response has totalSize = ${nConnections + 1}`]: (r) =>
        r.json().totalSize === nConnections + 1,
      [`GET ${pathWithFilter} response has default page size`]: (r) =>
        r.json().connections.length === defaultPageSize,
    });

    var pathWithToken = pathWithFilter + `&pageToken=${firstPage.json().nextPageToken}`;
    check(http.request("GET", pipelinePublicHost + pathWithToken, null, data.header), {
      [`GET ${pathWithToken} response status is 200`]: (r) => r.status === 200,
      [`GET ${pathWithToken} response has totalSize = ${nConnections + 1}`]: (r) =>
        r.json().totalSize === nConnections + 1,
      [`GET ${pathWithToken} response has remaining items`]: (r) =>
        r.json().connections.length === nConnections + 1 - defaultPageSize,
      [`GET ${pathWithToken} response has no more pages`]: (r) => r.json().nextPageToken === "",
    });

    // With integration ID filter
    var pathWithIntegration = pathWithFilter + `%20AND%20integrationId='${integrationID}'`;
    check(http.request("GET", pipelinePublicHost + pathWithIntegration, null, data.header), {
      [`GET ${pathWithIntegration} response status is 200`]: (r) => r.status === 200,
      [`GET ${pathWithIntegration} response has totalSize = ${nConnections}`]: (r) =>
        r.json().totalSize === nConnections,
      [`GET ${pathWithIntegration} response contains connections for ${integrationID} integration`]: (r) =>
        r.json().connections[0].integrationId === integrationID,
    });

  });

  group("Integration API: Update connection", () => {
    var path = `/v1beta/namespaces/${defaultUsername}/connections/${connectionID}`;
    var originalConn = http.request(
      "GET",
      pipelinePublicHost + path,
      null,
      data.header
    ).json().connection;

    var newPass = "4324";
    var req = http.request(
      "PATCH",
      pipelinePublicHost + path,
      JSON.stringify({
        uid: "should-be-ignored",
        setup: {
          "email-address": "wombat@instill.tech",
          password: newPass,
          "server-address": "localhost",
          "server-port": 993,
        },
      }),
      data.header
    );

    check(req, {
      [`PATCH ${path} response status 200`]: (r) => r.status === 200,
      [`PATCH ${path} contains new setup`]: (r) => r.json().connection.setup.password === newPass,
      [`PATCH ${path} didn't modify UID`]: (r) => r.json().connection.uid === originalConn.uid,
    });

    check(http.request("GET", pipelinePublicHost + path + "?view=VIEW_FULL", null, data.header), {
      [`GET ${path + "?view=VIEW_FULL"} response status is 200`]: (r) => r.status === 200,
      [`GET ${path + "?view=VIEW_FULL"} has new setup value`]: (r) =>
        r.json().connection.setup.password === newPass,
    });
  });

  group("Integration API: Delete connection", () => {
    var path = `/v1beta/namespaces/${defaultUsername}/connections/${connectionID}`;
    check(http.request("DELETE", pipelinePublicHost + path, null, data.header), {
      [`DELETE ${path} response status 204`]: (r) => r.status === 204,
    });

    check(http.request("GET", pipelinePublicHost + path, null, data.header), {
      [`GET ${path} response status is 404`]: (r) => r.status === 404,
    });

    check(http.request("DELETE", pipelinePublicHost + path, null, data.header), {
      [`DELETE ${path} response status 404`]: (r) => r.status === 404,
    });
  });
}