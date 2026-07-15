# Compose DingTalk text and images as Markdown

DingTalk deliveries containing text and an image will upload the image first and send one Markdown message containing the resulting media reference, regardless of whether delivery uses a temporary session endpoint or the proactive API. A fallback reuses the uploaded media ID so observable message shape and upload cost do not depend on endpoint availability.
