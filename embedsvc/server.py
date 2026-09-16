"""Sentence embeddings for the translation memory.

The Go service does not run an ONNX model itself, so it asks this process for
vectors and stores them in pgvector.
"""

from fastapi import FastAPI
from fastembed import TextEmbedding
from pydantic import BaseModel

MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"
DIMENSIONS = 384

app = FastAPI(title="localization-pipeline embeddings")
model = TextEmbedding(model_name=MODEL_NAME)


class EmbedRequest(BaseModel):
    texts: list[str]


@app.get("/health")
def health():
    return {"model": MODEL_NAME, "dimensions": DIMENSIONS}


@app.post("/embed")
def embed(req: EmbedRequest):
    vectors = [v.tolist() for v in model.embed(req.texts)]
    return {"model": MODEL_NAME, "dimensions": DIMENSIONS, "vectors": vectors}
