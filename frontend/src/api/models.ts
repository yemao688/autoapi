// Compatibility barrel: the v2 generated `go/models.ts` module exposed Go
// packages as TypeScript namespaces. The v3 generator emits one `models.ts`
// per Go package, so re-export them under the same namespace names.
export * as model from '../../bindings/autoapi/internal/model/models'
export * as api from '../../bindings/autoapi/internal/api/models'
export * as proxy from '../../bindings/autoapi/internal/proxy/models'
export * as service from '../../bindings/autoapi/internal/service/models'
export * as toolconfig from '../../bindings/autoapi/internal/toolconfig/models'
