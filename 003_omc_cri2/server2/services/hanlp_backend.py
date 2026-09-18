"""The original COARSE_ELECTRA_SMALL_ZH model with the original /parse output."""
import json
import os


class HanLP:
    def __init__(self, config):
        os.environ["HANLP_HOME"] = config["model_dir"]
        os.environ["HF_HUB_OFFLINE"] = "1"
        os.environ["TRANSFORMERS_OFFLINE"] = "1"
        os.environ.pop("HANLP_URL", None)
        import torch
        import hanlp
        torch.set_num_threads(config["threads"])
        torch.set_num_interop_threads(1)
        self.model = hanlp.load(hanlp.pretrained.tok.COARSE_ELECTRA_SMALL_ZH)
        self.model("测试")

    def __call__(self, body):
        text = json.loads(body)["text"]
        words = self.model(text) if text else []
        if not isinstance(words, list) or any(not isinstance(w, str) for w in words):
            raise ValueError("invalid HanLP output")
        return {"tok/fine": words, "tok/coarse": words}
