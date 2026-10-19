# Embedded report font

`DroidSansFallback.ttf` — vendored so exported report PDFs can embed a CJK font.

| | |
|---|---|
| Source | Android Open Source Project, `platform/frameworks_base/data/fonts/DroidSansFallback.ttf` |
| Licence | Apache License 2.0 — see `LICENSE-Apache-2.0.txt` |
| Why vendored | The backend container ships no CJK font, and the report PDF must render Chinese in browsers. Installing one through the package manager would tie PDF correctness to an apk name; embedding the file in the binary makes the build reproducible. |
| Why this face | It is a TrueType (glyf) font, which `/FontFile2` accepts directly, and it covers the CJK repertoire. It has **no Latin glyphs** by design (it is a *fallback* face) — the PDF keeps Helvetica for ASCII, which is one of the standard 14 fonts every reader has. |
| Size | 3.3 MB, embedded whole (compressed to ≈1.6 MB per PDF). Subsetting is the obvious next step; it was not done in the first pass because a malformed subset breaks the PDF in a new way. |
