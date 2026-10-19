// Swagger UI for /openapi.json, served by healthkit-relay. validatorUrl: null keeps the spec
// on this server (the default sends it to validator.swagger.io).
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
