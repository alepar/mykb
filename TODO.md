# TODO

## Revisit filesystem cache design

The filesystem cache (`~/.local/share/mykb/`, 2-level sharded) was originally
for raw web sources to avoid re-embedding/re-crawling. Its value has eroded
as the pipeline matured (Postgres holds metadata, Meilisearch holds full text,
Qdrant holds vectors). Wiki documents introduced via the llm-wiki design will
*not* be cached on the filesystem because the source-of-truth file already
lives locally in the vault repo. Worth a broader pass: do raw-source documents
still need the filesystem cache, or can it be removed entirely?
