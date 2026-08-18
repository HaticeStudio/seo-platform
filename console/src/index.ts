// Public entry of the embeddable package. Hosts render <SeoConsole/> inside
// their own admin shell and supply a generic AuthClient; the standalone shell
// in standalone.tsx is just another consumer of this same export.
export {
  completeOAuthCallback,
  OAUTH_PROVIDER_KEY,
  SeoConsole,
  type SeoConsoleOptions,
} from './SeoConsole'
export {
  ApiClient,
  ApiError,
  type AuthClient,
  type Connection,
  type ProviderDescriptor,
  type ReportRow,
  type SiteContext,
  type SyncRun,
} from './api'
