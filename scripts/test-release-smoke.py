#!/usr/bin/env python3
"""Smoke-test one already-built release image with disposable Docker resources.

Requires Python 3.9+ on the Linux Docker host, using its local Unix socket.
No production paths, provider
credentials, browser acceptance, or external network access are used.
"""

import argparse
import base64
import http.client
import ipaddress
import json
import os
import re
import secrets
import signal
import subprocess
import sys
import time


OWNER_LABEL = "org.erdai.release-smoke"
DATABASE = "/app/data/erdai-agent-core.sqlite3"


class SmokeFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def container_address(container, network, name, owner):
    require(network.get("Internal") is True and network.get("Driver") == "bridge",
            "network.Internal/Driver must be internal bridge")
    require((network.get("Labels") or {}).get(OWNER_LABEL) == owner, "network.Labels ownership differs")
    require((container.get("Config", {}).get("Labels") or {}).get(OWNER_LABEL) == owner,
            "container.Config.Labels ownership differs")
    attachments = container.get("NetworkSettings", {}).get("Networks", {})
    require(set(attachments) == {name}, "container.NetworkSettings.Networks differs")
    attachment = attachments[name]
    require(bool(network.get("Id")) and attachment.get("NetworkID") == network["Id"],
            "container.NetworkSettings.Networks.NetworkID differs")
    try:
        address = ipaddress.IPv4Address(attachment.get("IPAddress", ""))
    except ipaddress.AddressValueError:
        raise SmokeFailure("container.NetworkSettings.Networks.IPAddress is invalid") from None
    require(address.is_private and not (address.is_loopback or address.is_link_local or
            address.is_multicast or address.is_unspecified or address.is_reserved),
            "container.NetworkSettings.Networks.IPAddress is not private unicast")
    try:
        subnets = [ipaddress.ip_network(item["Subnet"]) for item in network.get("IPAM", {}).get("Config", [])
                   if item.get("Subnet")]
    except ValueError:
        raise SmokeFailure("network.IPAM.Config.Subnet is invalid") from None
    require(any(subnet.version == 4 and address in subnet and
                address not in (subnet.network_address, subnet.broadcast_address) for subnet in subnets),
            "container IPAddress is outside network.IPAM.Config.Subnet")
    member = network.get("Containers", {}).get(container.get("Id"), {})
    require(member.get("Name") == name and member.get("IPv4Address") ==
            str(address) + "/" + str(attachment.get("IPPrefixLen")),
            "network.Containers.IPv4Address membership differs")
    require(not any((container.get("HostConfig", {}).get("PortBindings") or {}).values()),
            "container.HostConfig.PortBindings must be empty")
    return str(address)


def smoke(image, version, schema):
    owner = secrets.token_hex(12)
    name = "erdai-release-smoke-" + owner
    resources = []
    report = {"ok": False, "image": image, "expectedVersion": version,
              "expectedSchema": schema, "checks": {}, "cleanup": [],
              "productionDataUsed": False, "externalNetworkEnabled": False,
              "connectionMethod": "host_to_internal_bridge", "publishedPorts": False}
    stage = "preflight"
    address = None
    environment = {key: value for key, value in os.environ.items()
                   if not key.startswith("ERDAI_")}
    environment.update({
        "ERDAI_ADMIN_TOKEN": secrets.token_urlsafe(48),
        "ERDAI_RUNTIME_TOKEN": secrets.token_urlsafe(48),
        "ERDAI_ADMIN_USERNAME": "smoke-" + secrets.token_hex(8),
        "ERDAI_ADMIN_PASSWORD": secrets.token_urlsafe(32),
        "ERDAI_RUN_ENCRYPTION_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "ERDAI_RUNTIME_IDENTITY_SECRET": secrets.token_urlsafe(48),
    })

    def docker(arguments, step, timeout=30, check=True):
        nonlocal stage
        stage = step
        try:
            result = subprocess.run(["docker", *arguments], env=environment,
                                    capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            raise SmokeFailure(step + ": docker timed out") from None
        except OSError:
            raise SmokeFailure(step + ": docker unavailable") from None
        if check and result.returncode:
            # Never emit Docker logs, inspect payloads, commands, or credentials.
            raise SmokeFailure(step + ": docker exit " + str(result.returncode))
        return result

    def inspect(kind, resource):
        value = json.loads(docker([kind, "inspect", resource], "inspect " + kind).stdout)
        require(len(value) == 1, "unexpected inspect result")
        return value[0]

    def request(port, path, expected=200, headers=None, payload=None):
        nonlocal stage
        stage = "HTTP " + path
        connection = http.client.HTTPConnection(address, port, timeout=10)
        try:
            request_headers = dict(headers or {})
            body = None
            if payload is not None:
                body = json.dumps(payload).encode()
                request_headers["Content-Type"] = "application/json"
            connection.request("POST" if body is not None else "GET", path,
                               body=body, headers=request_headers)
            response = connection.getresponse()
            data = response.read(4 * 1024 * 1024 + 1)
            require(response.status == expected, path + ": unexpected HTTP status")
            require(len(data) <= 4 * 1024 * 1024, path + ": oversized response")
            return data, response.getheaders()
        finally:
            connection.close()

    try:
        require(sys.platform.startswith("linux"), "smoke must run on the Linux Docker host")
        endpoint = environment.get("DOCKER_HOST", "")
        if not endpoint or environment.get("DOCKER_CONTEXT"):
            context = json.loads(docker(["context", "inspect"], "inspect Docker context").stdout)[0]
            endpoint = context.get("Endpoints", {}).get("docker", {}).get("Host", "")
        require(endpoint.startswith("unix:///"), "a local Docker Unix socket is required")
        metadata = json.loads(docker(["image", "inspect", image], "inspect image").stdout)[0]
        require(metadata.get("Os") == "linux", "release image must target Linux")
        require(metadata.get("Config", {}).get("Healthcheck", {}).get("Test") ==
                ["CMD", "/app/erdai-agent", "--health-check"], "image healthcheck differs")
        require(metadata.get("Config", {}).get("User") == "1000:1000", "image user differs")
        report["imageId"] = metadata["Id"]
        resources.append(("network", name))
        docker(["network", "create", "--internal", "--label", OWNER_LABEL + "=" + owner, name],
               "create internal network")
        require(inspect("network", name).get("Internal") is True, "network is not internal")
        resources.append(("volume", name))
        docker(["volume", "create", "--label", OWNER_LABEL + "=" + owner, name], "create fresh volume")
        resources.append(("container", name))
        command = ["create", "--name", name, "--pull=never", "--label", OWNER_LABEL + "=" + owner,
                   "--network", name,
                   "--mount", "type=volume,source=" + name + ",target=/app/data",
                   "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
                   "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                   "--cpus", "1", "--memory", "512m", "--memory-swap", "512m", "--pids-limit", "128",
                   "--health-interval", "2s", "--health-timeout", "5s", "--health-start-period", "5s",
                   "--health-retries", "30", "--env", "GOMAXPROCS=1", "--env", "GOMEMLIMIT=384MiB"]
        for key in sorted(environment):
            if key.startswith("ERDAI_"):
                command += ["--env", key]
        # Secrets are inherited by name, never passed in CLI arguments or files.
        docker([*command, metadata["Id"]], "create isolated container")
        docker(["start", name], "start isolated container")
        deadline = time.monotonic() + 150
        while True:
            container = inspect("container", name)
            state = container["State"]
            require(state.get("Running") is True and not state.get("OOMKilled"), "container stopped or OOM-killed")
            if state.get("Health", {}).get("Status") == "healthy":
                break
            require(time.monotonic() < deadline, "container health deadline exceeded")
            time.sleep(1)
        require(container.get("RestartCount") == 0, "container unexpectedly restarted")
        report["checks"]["containerHealthy"] = True
        network = inspect("network", name)
        stage = "validate internal bridge address"
        address = container_address(container, network, name, owner)
        report["checks"]["ownedInternalBridgeAddress"] = True
        for port in (6280, 6282):
            require(json.loads(request(port, "/healthz")[0]).get("ok") is True, "health payload differs")
        report["checks"]["bothHealthEndpoints"] = True
        page, _ = request(6282, "/")
        require(b"<html" in page.lower() and b"<script" in page.lower(), "embedded homepage missing")
        report["checks"]["embeddedHomepage"] = True
        request(6282, "/api/v1/overview", expected=401)
        request(6280, "/api/v1/overview", expected=404)
        report["checks"]["anonymousManagementRejected"] = True
        login, login_headers = request(6282, "/auth/login", payload={
            "username": environment["ERDAI_ADMIN_USERNAME"], "password": environment["ERDAI_ADMIN_PASSWORD"]})
        require(json.loads(login).get("data", {}).get("authenticated") is True, "administrator login failed")
        cookies = [value.split(";", 1)[0] for key, value in login_headers
                   if key.lower() == "set-cookie" and value.startswith("erdai_admin_session=")]
        require(len(cookies) == 1, "administrator session cookie missing")
        overview = json.loads(request(6282, "/api/v1/overview", headers={"Cookie": cookies[0]})[0])
        actual_schema = overview.get("data", {}).get("schemaVersion")
        require(type(actual_schema) is int and actual_schema == schema, "schema version mismatch")
        report["checks"]["administratorSession"] = True
        report["schemaVersion"] = actual_schema
        registry = json.loads(request(6282, "/api/v1/integrations/channel_platforms",
                                      headers={"X-Erdai-Admin-Token": environment["ERDAI_ADMIN_TOKEN"]})[0])
        actual_version = registry.get("data", {}).get("config", {}).get("runtimeVersion")
        require(actual_version == version, "runtime version mismatch")
        report["runtimeVersion"] = actual_version
        result = docker(["exec", name, "/app/erdai-agent", "--check-sqlite", DATABASE],
                        "read-only database integrity check", timeout=30)
        require(result.stdout.strip() == "ok", "database integrity result differs")
        report["checks"]["sqliteIntegrityCLI"] = True
        report["ok"] = True
    except SmokeFailure as error:
        report["error"] = str(error)
        report["errorStage"] = stage
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = "smoke interrupted" if isinstance(error, KeyboardInterrupt) else type(error).__name__
        report["errorStage"] = stage
        if isinstance(error, KeyError) and error.args[0] in {"Id", "State", "Subnet", "HostPort", "NetworkSettings"}:
            report["errorField"] = error.args[0]
    finally:
        for kind, resource in reversed(resources):
            cleanup = {"kind": kind, "name": resource, "removed": False}
            try:
                metadata = inspect(kind, resource)
                labels = metadata.get("Config", {}).get("Labels", {}) if kind == "container" else metadata.get("Labels", {})
                require((labels or {}).get(OWNER_LABEL) == owner, "cleanup ownership label differs")
                command = [kind, "rm"] + (["--force"] if kind == "container" else []) + [resource]
                docker(command, "remove owned " + kind)
                cleanup["removed"] = True
            except (Exception, KeyboardInterrupt):
                report["ok"] = False
                cleanup["error"] = "owned resource cleanup not confirmed"
            report["cleanup"].append(cleanup)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="Already-built local release image; never pulled")
    parser.add_argument("--version", required=True, help="Expected runtime version, optionally prefixed with v")
    parser.add_argument("--schema", type=int, required=True, help="Expected core database schema")
    args = parser.parse_args()
    args.version = args.version.removeprefix("v")
    if not args.image or args.image.startswith("-") or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", args.version) or args.schema < 1:
        parser.error("image, semantic version, and positive schema are required")
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt()

    signal.signal(signal.SIGTERM, interrupted)
    result = smoke(args.image, args.version, args.schema)
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
