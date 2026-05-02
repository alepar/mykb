# MyKB Ingest — Firefox Extension

Toolbar button that sends the current page's URL to the MyKB API for ingestion.

## Install (temporary)

The extension is unsigned, so Firefox only allows loading it as a temporary add-on:

1. Open `about:debugging#/runtime/this-firefox` in Firefox.
2. Click **Load Temporary Add-on…**.
3. Select `extension/manifest.json` from this repo.

The extension stays loaded until Firefox restarts. After a restart, repeat the steps to reload it.

## Configure

Right-click the brain icon in the toolbar → **Manage Extension** → **Preferences** (Options) to set the MyKB API endpoint.

## Use

Click the brain icon on any page to ingest it.

## Permanent install

For a non-temporary install you need to package the extension as an `.xpi` and either:

- submit it to [addons.mozilla.org](https://addons.mozilla.org/) for signing, or
- use Firefox Developer Edition, Nightly, or ESR with `xpinstall.signatures.required=false` in `about:config`.
