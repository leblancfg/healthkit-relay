// Swagger UI for /openapi.json, served by healthkit-relay. No requests leave the server:
// validatorUrl is off (the default sends the spec to validator.swagger.io).
window.ui = SwaggerUIBundle({
  url: "openapi.json",
  dom_id: "#swagger-ui",
  deepLinking: true,
  validatorUrl: null,
  persistAuthorization: false,
  displayRequestDuration: true,
  defaultModelsExpandDepth: 0,
  presets: [SwaggerUIBundle.presets.apis],
  layout: "BaseLayout",
});
