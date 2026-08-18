-- Bind the post-authorization destination to the short-lived OAuth state.
-- The value is a local Console path only; it can never be an external URL.

ALTER TABLE oauth_states ADD COLUMN return_to TEXT NOT NULL DEFAULT '/';
