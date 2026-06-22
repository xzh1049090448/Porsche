"""Domain-specific exceptions."""


class UpstreamError(Exception):
    """Upstream provider returned an error or unreachable."""

    def __init__(self, status_code: int, detail: str) -> None:
        self.status_code = status_code
        self.detail = detail
        super().__init__(detail)
