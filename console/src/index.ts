// Public entry of the embeddable package. Hosts render <SeoConsole/> inside
// their own admin shell. Same-origin host sessions work without an AuthClient;
// the standalone shell in standalone.tsx is just another consumer.
export {
  completeOAuthCallback,
  completeOAuthCallbackWithResult,
  OAUTH_PROVIDER_KEY,
  SeoConsole,
  type SeoConsoleOptions,
  type OAuthCallbackResult,
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
