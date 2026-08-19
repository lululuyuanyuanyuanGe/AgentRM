import json
from typing import Any, Dict, Optional
from urllib import error, request

from .models import ResourceEstimate


class AgentRMError(RuntimeError):
    pass


class AgentRMClient:
    def __init__(self, base_url: str = "http://127.0.0.1:8080", timeout: float = 5.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def create_session(
        self,
        session_id: str,
        min_cpu_milli: int,
        min_memory_bytes: int,
        max_cpu_milli: int,
        max_memory_bytes: int,
        priority: int = 1,
    ) -> Dict[str, Any]:
        return self._request(
            "POST",
            "/v1/sessions",
            {
                "session_id": session_id,
                "min_resource": {
                    "cpu_milli": min_cpu_milli,
                    "memory_bytes": min_memory_bytes,
                },
                "max_resource": {
                    "cpu_milli": max_cpu_milli,
                    "memory_bytes": max_memory_bytes,
                },
                "task_priority": priority,
            },
        )

    def update_state(self, session_id: str, state: str, priority: int = 1) -> Dict[str, Any]:
        return self._request(
            "PATCH",
            "/v1/sessions/{}/state".format(session_id),
            {"session_state": state, "task_priority": priority},
        )

    def request_resources(
        self,
        session_id: str,
        estimate: ResourceEstimate,
        generation: int,
        priority: int = 1,
    ) -> Dict[str, Any]:
        return self._request(
            "POST",
            "/v1/sessions/{}/resources".format(session_id),
            {
                "desired_resource": estimate.as_api_resource(),
                "generation": generation,
                "priority": priority,
            },
        )

    def get_session(self, session_id: str) -> Dict[str, Any]:
        return self._request("GET", "/v1/sessions/{}".format(session_id))

    def suspend(self, session_id: str) -> Dict[str, Any]:
        return self._request("POST", "/v1/sessions/{}/suspend".format(session_id), {})

    def resume(self, session_id: str) -> Dict[str, Any]:
        return self._request("POST", "/v1/sessions/{}/resume".format(session_id), {})

    def _request(
        self, method: str, path: str, payload: Optional[Dict[str, Any]] = None
    ) -> Dict[str, Any]:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        http_request = request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        try:
            with request.urlopen(http_request, timeout=self.timeout) as response:
                return json.loads(response.read().decode("utf-8"))
        except error.HTTPError as exc:
            details = exc.read().decode("utf-8")
            raise AgentRMError("AgentRM returned {}: {}".format(exc.code, details)) from exc
        except error.URLError as exc:
            raise AgentRMError("cannot reach AgentRM: {}".format(exc.reason)) from exc

