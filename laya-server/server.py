import json
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import Dict, Any, List
import laya
from laya import Router

app = FastAPI(title="Laya Jev-Compatible API")

import torch

# Initialize Laya Router with preload=True for fast routing
print("Loading Laya Checkpoints into memory (this may take a minute on first run)...")
device = "cuda" if torch.cuda.is_available() else "cpu"
router = Router(preload=True, device=device)
print(f"Laya Checkpoints Loaded successfully on {device}!")

class EvaluationRequest(BaseModel):
    state: Dict[str, Any]
    questions: List[str]

# Define the questions based on the Reflex requirements
QUESTIONS = {
    "domain": {
        "type": "choice",
        "instructions": "Is this a database, network, or application issue?",
        "criteria": {
            "database": "database issue, query errors, connection refused",
            "network": "network timeout, dropped packets, DNS",
            "application": "application crash, 500 errors",
            "cluster_ops": "kubernetes, pods crashing, node down"
        }
    },
    "severity": {
        "type": "score",
        "instructions": "Rate the blast radius from 1.0 to 10.0.",
        "criteria": ["low impact", "medium", "critical outage"]
    },
    "auto_recoverable": {
        "type": "noul",
        "instructions": "Is this a known transient error we can safely restart?"
    }
}

@app.post("/v1/evaluate")
def evaluate(req: EvaluationRequest):
    try:
        # Filter questions down to what was requested
        req_questions = {k: v for k, v in QUESTIONS.items() if k in req.questions}
        if not req_questions:
            req_questions = QUESTIONS
            
        # Run through Laya
        res = router.predict(req.state, req_questions)
        
        # Format exactly like Jev
        answers = res.get("answers", {})
        
        # Laya uses the "noul" key for Noul type questions, Jev expects "probability"
        for q_name, ans in answers.items():
            if ans.get("type") == "noul" and "noul" in ans:
                ans["probability"] = ans["noul"]

        return {
            "answers": answers,
            "usage": {
                "input_tokens": 0,
                "output_tokens": 0
            }
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8081)
