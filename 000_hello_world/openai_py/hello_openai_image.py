from hello_openai import read_api_key
from openai import OpenAI
from openai.types.responses import (
    EasyInputMessageParam,
    ResponseInputImageParam,
    ResponseInputTextParam,
)


def main() -> None:
    client: OpenAI = OpenAI(api_key=read_api_key())
    response = client.responses.create(
        model="gpt-5.4-mini",
        input=[
            EasyInputMessageParam(
                role="user",
                content=[
                    ResponseInputTextParam(
                        type="input_text",
                        text="What teams are playing in this image?",
                    ),
                    ResponseInputImageParam(
                        type="input_image",
                        detail="auto",
                        image_url="https://api.nga.gov/iiif/a2e6da57-3cd1-4235-b20e-95dcaefed6c8/full/!800,800/0/default.jpg",
                    ),
                ],
            )
        ],
    )
    # "This image doesn’t show any sports teams. It appears to be a painted
    #  portrait of a woman seated in a chair."
    print(response.output_text)


if __name__ == "__main__":
    main()
