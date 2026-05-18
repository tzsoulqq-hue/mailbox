from __future__ import annotations

import logging
import os
import signal
import threading
from concurrent import futures
from urllib.parse import urlparse

import grpc

import email_pb2
import email_pb2_grpc
import mailbox_register_pb2
import mailbox_register_pb2_grpc
import register_provider


DEFAULT_LISTEN_ADDR = ":50051"
DEFAULT_EMAIL_SERVICE_ADDR = "outlook-imap-service:50051"
EMAIL_AUTHORIZED = "AUTHORIZED"
EMAIL_OAUTH_PENDING = "OAUTH_PENDING"
EMAIL_AUTH_FAILED = "AUTH_FAILED"
EMAIL_NEEDS_MANUAL_VERIFY = "NEEDS_MANUAL_VERIFICATION"

logger = logging.getLogger("outlook-register-service")


def env_str(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def grpc_listen_addr(value: str) -> str:
    value = value or DEFAULT_LISTEN_ADDR
    if value.startswith(":"):
        return "[::]" + value
    parsed = urlparse("//" + value)
    if parsed.hostname and parsed.port:
        return value
    if parsed.hostname and not parsed.port:
        return value + ":50051"
    return value


def env_int(name: str, default: int) -> int:
    value = env_str(name)
    if not value:
        return default
    try:
        return int(value)
    except ValueError:
        logger.warning("%s=%s is not an integer; using %s", name, value, default)
        return default


def normalize_email(value: str) -> str:
    return (value or "").strip().lower()


def mailbox_auth_status(refresh_token: str, explicit_status: str = "") -> str:
    explicit_status = (explicit_status or "").strip()
    if explicit_status:
        return explicit_status
    if (refresh_token or "").strip():
        return EMAIL_AUTHORIZED
    return EMAIL_OAUTH_PENDING


def mailbox_oauth_failure_status(error_message: str) -> str:
    error_text = (error_message or "").strip().lower()
    if "needs_manual_verification" in error_text or "account.live.com/abuse" in error_text:
        return EMAIL_NEEDS_MANUAL_VERIFY
    return EMAIL_AUTH_FAILED


class EmailStore:
    def __init__(self) -> None:
        self.addr = env_str("MAILBOX_EMAIL_SERVICE_ADDR", env_str("EMAIL_ADDR", DEFAULT_EMAIL_SERVICE_ADDR))
        self.timeout_seconds = max(env_int("MAILBOX_EMAIL_SERVICE_TIMEOUT_SECONDS", 10), 1)

    def _call(self, operation):
        channel = grpc.insecure_channel(self.addr)
        try:
            grpc.channel_ready_future(channel).result(timeout=self.timeout_seconds)
            return operation(email_pb2_grpc.EmailServiceStub(channel))
        finally:
            channel.close()

    def upsert_account(self, account: dict) -> None:
        email = normalize_email(account.get("email_address", ""))
        password = (account.get("password", "") or "").strip()
        refresh_token = (account.get("refresh_token", "") or "").strip()
        access_token = (account.get("access_token", "") or "").strip()
        if not email:
            raise ValueError("mailbox account missing email_address")
        if not password:
            raise ValueError(f"mailbox account missing password: {email}")

        mailbox = email_pb2.EmailMailbox(
            email_address=email,
            password=password,
            refresh_token=refresh_token,
            access_token=access_token,
            auth_status=mailbox_auth_status(refresh_token),
            last_error="",
            is_primary=True,
            primary_email=email,
        )
        self._call(lambda stub: stub.UpsertMailbox(email_pb2.UpsertEmailMailboxRequest(mailbox=mailbox)))

    def mark_auth_status(self, email: str, auth_status: str, last_error: str = "") -> None:
        email = normalize_email(email)
        if not email:
            return
        self._call(
            lambda stub: stub.MarkEmailAuthStatus(
                email_pb2.MarkEmailAuthStatusRequest(
                    email_address=email,
                    auth_status=auth_status,
                    last_error=last_error or "",
                )
            )
        )

    def oauth_accounts(self, email_address: str, only_missing: bool, limit: int) -> list[dict]:
        requested_email = normalize_email(email_address)
        selected_limit = limit if limit > 0 else 100
        selected_limit = min(selected_limit, 500)
        if requested_email:
            selected_limit = 500

        def list_mailboxes(stub):
            return stub.ListMailboxes(email_pb2.ListEmailMailboxesRequest(limit=selected_limit))

        response = self._call(list_mailboxes)
        accounts: list[dict] = []
        for mailbox in response.mailboxes:
            email = normalize_email(mailbox.email_address)
            if not email:
                continue
            if requested_email and email != requested_email:
                continue
            if not mailbox.is_primary:
                continue
            if not (mailbox.password or "").strip():
                continue
            auth_status = mailbox_auth_status(mailbox.refresh_token, mailbox.auth_status)
            if only_missing and auth_status in {EMAIL_AUTHORIZED, EMAIL_NEEDS_MANUAL_VERIFY}:
                continue
            accounts.append(
                {
                    "email_address": email,
                    "password": (mailbox.password or "").strip(),
                    "refresh_token": (mailbox.refresh_token or "").strip(),
                    "access_token": (mailbox.access_token or "").strip(),
                    "source": "mailboxes",
                }
            )
            if not requested_email and len(accounts) >= selected_limit:
                break
        if requested_email and not accounts:
            raise ValueError(f"mailbox not found or not eligible for OAuth: {requested_email}")
        return accounts

    def apply_oauth_result(self, item: dict, password_by_email: dict[str, str]) -> None:
        email = normalize_email(item.get("email_address", ""))
        if not email:
            return
        refresh_token = (item.get("refresh_token", "") or "").strip()
        access_token = (item.get("access_token", "") or "").strip()
        if bool(item.get("success")) and refresh_token:
            mailbox = email_pb2.EmailMailbox(
                email_address=email,
                password=password_by_email.get(email, ""),
                refresh_token=refresh_token,
                access_token=access_token,
                auth_status=EMAIL_AUTHORIZED,
                last_error="",
                is_primary=True,
                primary_email=email,
            )
            self._call(lambda stub: stub.UpsertMailbox(email_pb2.UpsertEmailMailboxRequest(mailbox=mailbox)))
            return
        if not bool(item.get("success")):
            error_message = item.get("error_message", "") or ""
            self.mark_auth_status(email, mailbox_oauth_failure_status(error_message), error_message)


class MailboxRegistrationService(mailbox_register_pb2_grpc.MailboxRegistrationServiceServicer):
    def RunMailboxRegistration(self, request, context):
        del context
        try:
            result = register_provider.run_registration_request(
                enabled=bool(request.enabled),
                import_only=bool(request.import_only),
            )
            accounts = [
                mailbox_register_pb2.MailboxRegistrationAccount(
                    email_address=item.get("email_address", ""),
                    password=item.get("password", ""),
                    refresh_token=item.get("refresh_token", ""),
                    access_token=item.get("access_token", ""),
                    source=item.get("source", ""),
                )
                for item in result.get("accounts", [])
            ]
            success = bool(result.get("success")) and len(accounts) > 0
            error_message = result.get("error_message", "")
            if not success and not error_message:
                error_message = "mailbox registration returned no accounts"
            if success:
                store = EmailStore()
                for item in result.get("accounts", []):
                    store.upsert_account(item)
            return mailbox_register_pb2.RunMailboxRegistrationResponse(
                success=success,
                exit_code=int(result.get("exit_code", 1)),
                error_message=error_message,
                accounts=accounts,
            )
        except Exception as err:
            logger.exception("mailbox registration failed")
            return mailbox_register_pb2.RunMailboxRegistrationResponse(
                success=False,
                exit_code=1,
                error_message=str(err),
            )

    def RunMailboxOAuth(self, request, context):
        del context
        try:
            accounts = [
                {
                    "email_address": account.email_address,
                    "password": account.password,
                    "refresh_token": account.refresh_token,
                    "access_token": account.access_token,
                    "source": account.source,
                }
                for account in request.accounts
            ]
            store = EmailStore()
            if not accounts:
                accounts = store.oauth_accounts(
                    email_address=request.email_address,
                    only_missing=bool(request.only_missing),
                    limit=int(request.limit),
                )
            result = register_provider.run_oauth(
                email_address=request.email_address,
                only_missing=request.only_missing,
                limit=request.limit,
                accounts=accounts,
            )
            results = [
                mailbox_register_pb2.MailboxOAuthResult(
                    email_address=item.get("email_address", ""),
                    success=bool(item.get("success")),
                    error_message=item.get("error_message", ""),
                    refresh_token=item.get("refresh_token", ""),
                    access_token=item.get("access_token", ""),
                )
                for item in result.get("results", [])
            ]
            password_by_email = {
                normalize_email(account.get("email_address", "")): (account.get("password", "") or "").strip()
                for account in accounts
            }
            for item in result.get("results", []):
                store.apply_oauth_result(item, password_by_email)
            return mailbox_register_pb2.RunMailboxOAuthResponse(
                success=bool(result.get("success")),
                processed=int(result.get("processed", 0)),
                succeeded=int(result.get("succeeded", 0)),
                failed=int(result.get("failed", 0)),
                error_message=result.get("error_message", ""),
                results=results,
            )
        except Exception as err:
            logger.exception("mailbox OAuth failed")
            return mailbox_register_pb2.RunMailboxOAuthResponse(success=False, error_message=str(err))


def serve() -> None:
    register_provider.configure_logging()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    mailbox_register_pb2_grpc.add_MailboxRegistrationServiceServicer_to_server(
        MailboxRegistrationService(),
        server,
    )
    listen_addr = grpc_listen_addr(env_str("LISTEN_ADDR", DEFAULT_LISTEN_ADDR))
    server.add_insecure_port(listen_addr)
    server.start()
    logger.info("Starting Outlook mailbox registration gRPC server on %s", listen_addr)

    stop_event = threading.Event()

    def stop(signum, frame):
        del frame
        logger.info("received signal %s; stopping", signum)
        server.stop(5)
        stop_event.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    stop_event.wait()


if __name__ == "__main__":
    serve()
