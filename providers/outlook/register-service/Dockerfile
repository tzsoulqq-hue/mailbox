FROM nb-register-camoufox-base:latest

WORKDIR /app

COPY proto/mailbox_register.proto /app/proto/mailbox_register.proto
RUN python -m grpc_tools.protoc -I /app/proto --python_out=/app --grpc_python_out=/app /app/proto/mailbox_register.proto

COPY providers/outlook/register-service/register_provider.py /app/register_provider.py
COPY providers/outlook/register-service/register_service.py /app/register_service.py
COPY providers/outlook/register-service/camoufox_register.py /app/camoufox_register.py
COPY providers/outlook/register-service/patch_outlook_register.py /app/patch_outlook_register.py

RUN mkdir -p /app/Results

ENV OUTLOOK_REGISTER_DIR=/app
ENV OUTLOOK_REGISTER_RESULTS_DIR=/app/Results

EXPOSE 50051

ENTRYPOINT ["python", "/app/register_service.py"]
