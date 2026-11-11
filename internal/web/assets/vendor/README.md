# Vendored Markdown parser

`marked.mjs` is the unmodified `lib/marked.esm.js` from the `marked@16.4.2`
npm package, copied from the local dependency cache. No frontend build or CDN
request is needed at runtime.

- Upstream: https://github.com/markedjs/marked/tree/v16.4.2
- Package: https://registry.npmjs.org/marked/-/marked-16.4.2.tgz
- License: [marked-LICENSE.md](marked-LICENSE.md)
- SHA-256: `6540aa2544be84b3abdd9424fa287120e9ed7c7600512dd92c097493121a5951`

The application owns the HTML and URL restrictions in `../markdown.mjs`;
Marked itself does not sanitize HTML.

## Time-zone conversion

`temporal.mjs` is `@js-temporal/polyfill@0.5.1`'s `dist/index.esm.js`,
copied from the local dependency cache, with the sole code change
`from"jsbi"` → `from"./jsbi.mjs"`. `jsbi.mjs` is the unmodified
`jsbi@4.3.2` `dist/jsbi.mjs`. Both are served locally and embedded in the Go
binary; no CDN, runtime npm installation or frontend build is needed.

- Temporal upstream: https://github.com/js-temporal/temporal-polyfill/tree/0.5.1
- Temporal license: [temporal-LICENSE](temporal-LICENSE), ISC
- Temporal vendored SHA-256: `ba415078da7202e8691816e4e0c90786c462da7490701eb32628c1b095668d8b`
- JSBI upstream: https://github.com/GoogleChromeLabs/jsbi/tree/v4.3.2
- JSBI license: [jsbi-LICENSE](jsbi-LICENSE), Apache-2.0
- JSBI SHA-256: `c0d70fb47e0818e31bdf964805a530d9a0fb4ee5bdadb442a13f3691a5f15583`

`../time.mjs` uses Temporal with `disambiguation:"reject"` to reject skipped
and repeated DST wall times. Display formatting uses explicit `Intl` time zones.
