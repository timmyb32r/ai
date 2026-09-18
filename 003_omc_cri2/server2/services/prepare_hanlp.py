"""Explicit one-off network-enabled preparation; never part of runtime startup."""
import os
os.environ.pop("HANLP_URL", None)
import hanlp

model = hanlp.load(hanlp.pretrained.tok.COARSE_ELECTRA_SMALL_ZH)
print(model("欢迎收听国际广播电台"))
print("HanLP assets ready in", os.environ["HANLP_HOME"])
