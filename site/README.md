# terminatr.dev

The project's website: static HTML, CSS and a little JavaScript, no build step. Vercel serves this folder as is (Root Directory `site`); `vercel.json` sets the headers and skips a deploy when nothing under `site/` changed. CI skips its Go jobs for pull requests that only touch `site/`.

The screens are tm's own: the end-to-end tests' golden captures (`internal/e2e/testdata/golden`), coloured and written into `index.html` by `tools/screens.py`. After a golden changes, run it again from the repository root and commit the result:

```sh
python3 site/tools/screens.py
```

It fills the captures' masks (`<duration>`, `<session>`, …) with neutral values and stops if one is left, or if a screen shows a home path.

To look at it locally: `python3 -m http.server -d site 8000`, then http://localhost:8000.
