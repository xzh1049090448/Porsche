"""RAG retrieval engine using Chroma vector store."""

from __future__ import annotations

from pathlib import Path

import chromadb
from langchain_text_splitters import RecursiveCharacterTextSplitter
from loguru import logger

DATASET_ATTRIBUTION = "本回答基于已确权跨境电商专属数据集生成"

RAG_SYSTEM_PREFIX = (
  "你是跨境电商领域专业助手。以下是从已确权专属数据集中检索到的相关知识，"
  "请优先参考这些内容回答用户问题。若知识库中没有相关信息，请基于通用知识回答并说明。\n\n"
  "【检索到的相关知识】\n"
)


class RagEngine:
  def __init__(self, persist_dir: str, chunk_size: int, chunk_overlap: int, top_k: int) -> None:
    self._persist_dir = Path(persist_dir)
    self._persist_dir.mkdir(parents=True, exist_ok=True)
    self._client = chromadb.PersistentClient(path=str(self._persist_dir))
    self._splitter = RecursiveCharacterTextSplitter(
      chunk_size=chunk_size,
      chunk_overlap=chunk_overlap,
      length_function=len,
    )
    self._top_k = top_k

  def _collection_name(self, dataset_id: int) -> str:
    return f"dataset_{dataset_id}"

  def index_documents(
    self,
    dataset_id: int,
    documents: list[str],
    metadatas: list[dict] | None = None,
  ) -> int:
    collection = self._client.get_or_create_collection(
      name=self._collection_name(dataset_id),
      metadata={"hnsw:space": "cosine"},
    )
    all_chunks: list[str] = []
    all_meta: list[dict] = []
    for i, doc in enumerate(documents):
      chunks = self._splitter.split_text(doc)
      for j, chunk in enumerate(chunks):
        all_chunks.append(chunk)
        meta = {"source_index": i, "chunk_index": j}
        if metadatas and i < len(metadatas):
          meta.update(metadatas[i])
        all_meta.append(meta)
    if not all_chunks:
      return 0
    ids = [f"{dataset_id}_{i}" for i in range(len(all_chunks))]
    collection.upsert(documents=all_chunks, metadatas=all_meta, ids=ids)
    logger.info("Indexed {} chunks for dataset {}", len(all_chunks), dataset_id)
    return len(all_chunks)

  def retrieve(self, dataset_ids: list[int], query: str) -> list[str]:
    results: list[str] = []
    for ds_id in dataset_ids:
      try:
        collection = self._client.get_collection(name=self._collection_name(ds_id))
      except Exception:
        logger.warning("Collection not found for dataset {}", ds_id)
        continue
      hits = collection.query(query_texts=[query], n_results=self._top_k)
      docs = hits.get("documents", [[]])[0]
      results.extend(docs)
    return results

  def build_rag_messages(
    self,
    messages: list[dict],
    dataset_ids: list[int],
    query: str,
  ) -> tuple[list[dict], bool]:
    chunks = self.retrieve(dataset_ids, query)
    if not chunks:
      return messages, False
    context = "\n\n".join(f"- {c}" for c in chunks)
    rag_system = RAG_SYSTEM_PREFIX + context
    new_messages = [{"role": "system", "content": rag_system}] + messages
    return new_messages, True

  def delete_collection(self, dataset_id: int) -> None:
    try:
      self._client.delete_collection(name=self._collection_name(dataset_id))
    except Exception:
      pass
