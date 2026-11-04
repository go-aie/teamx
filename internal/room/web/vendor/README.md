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
