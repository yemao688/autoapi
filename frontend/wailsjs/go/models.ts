export namespace api {
	
	export class ExportResult {
	    filename: string;
	    data: number[];
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filename = source["filename"];
	        this.data = source["data"];
	    }
	}
	export class ProxyStatus {
	    running: boolean;
	    url?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.url = source["url"];
	    }
	}

}

export namespace model {
	
	export class AdvancedSettings {
	    debug_mode: boolean;
	    experimental: boolean;
	    http_proxy: string;
	
	    static createFrom(source: any = {}) {
	        return new AdvancedSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.debug_mode = source["debug_mode"];
	        this.experimental = source["experimental"];
	        this.http_proxy = source["http_proxy"];
	    }
	}
	export class ApiKey {
	    id: string;
	    name: string;
	    expires_at: number;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ApiKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.expires_at = source["expires_at"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ApiKeyInput {
	    name: string;
	    expires_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ApiKeyInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class AppearanceSettings {
	    theme: string;
	    density: string;
	    accent_color: string;
	
	    static createFrom(source: any = {}) {
	        return new AppearanceSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.density = source["density"];
	        this.accent_color = source["accent_color"];
	    }
	}
	export class ProviderShare {
	    provider_id: string;
	    provider_name: string;
	    tokens: number;
	    cost: number;
	    percent: number;
	
	    static createFrom(source: any = {}) {
	        return new ProviderShare(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.tokens = source["tokens"];
	        this.cost = source["cost"];
	        this.percent = source["percent"];
	    }
	}
	export class StatusBreakdown {
	    label: string;
	    count: number;
	    percent: number;
	
	    static createFrom(source: any = {}) {
	        return new StatusBreakdown(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.count = source["count"];
	        this.percent = source["percent"];
	    }
	}
	export class TimeBucket {
	    bucket: string;
	    success: number;
	    rate_limited: number;
	    error: number;
	    input_tokens: number;
	    output_tokens: number;
	    cost: number;
	    avg_latency_ms: number;
	    avg_ttft_ms: number;
	
	    static createFrom(source: any = {}) {
	        return new TimeBucket(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bucket = source["bucket"];
	        this.success = source["success"];
	        this.rate_limited = source["rate_limited"];
	        this.error = source["error"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost = source["cost"];
	        this.avg_latency_ms = source["avg_latency_ms"];
	        this.avg_ttft_ms = source["avg_ttft_ms"];
	    }
	}
	export class ChartAggregates {
	    range: string;
	    bucket_size: string;
	    buckets: TimeBucket[];
	    status_breakdown: StatusBreakdown[];
	    provider_shares: ProviderShare[];
	
	    static createFrom(source: any = {}) {
	        return new ChartAggregates(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.range = source["range"];
	        this.bucket_size = source["bucket_size"];
	        this.buckets = this.convertValues(source["buckets"], TimeBucket);
	        this.status_breakdown = this.convertValues(source["status_breakdown"], StatusBreakdown);
	        this.provider_shares = this.convertValues(source["provider_shares"], ProviderShare);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChartQuery {
	    start_date: number;
	    end_date: number;
	    provider: string;
	    route_id: string;
	    model: string;
	    search: string;
	
	    static createFrom(source: any = {}) {
	        return new ChartQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.provider = source["provider"];
	        this.route_id = source["route_id"];
	        this.model = source["model"];
	        this.search = source["search"];
	    }
	}
	export class ServiceHealth {
	    status: string;
	    uptime_seconds: number;
	    cpu_percent: number;
	    memory_mb: number;
	    active_connections: number;
	    websocket_count: number;
	    http_count: number;
	    proxy_url: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceHealth(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.uptime_seconds = source["uptime_seconds"];
	        this.cpu_percent = source["cpu_percent"];
	        this.memory_mb = source["memory_mb"];
	        this.active_connections = source["active_connections"];
	        this.websocket_count = source["websocket_count"];
	        this.http_count = source["http_count"];
	        this.proxy_url = source["proxy_url"];
	    }
	}
	export class RequestLog {
	    id: string;
	    timestamp: number;
	    status_code: number;
	    provider_id: string;
	    provider_name: string;
	    model: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost: number;
	    latency_ms: number;
	    first_token_ms: number;
	    is_stream: boolean;
	    route_id: string;
	    route_label: string;
	    api_key_id: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new RequestLog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.timestamp = source["timestamp"];
	        this.status_code = source["status_code"];
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.model = source["model"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost = source["cost"];
	        this.latency_ms = source["latency_ms"];
	        this.first_token_ms = source["first_token_ms"];
	        this.is_stream = source["is_stream"];
	        this.route_id = source["route_id"];
	        this.route_label = source["route_label"];
	        this.api_key_id = source["api_key_id"];
	        this.error = source["error"];
	    }
	}
	export class Provider {
	    id: string;
	    name: string;
	    base_url: string;
	    status: string;
	    key_masked: string;
	    models_count: number;
	    monthly_tokens: number;
	    avg_latency_ms: number;
	    last_tested_at: number;
	    error_message?: string;
	    is_custom: boolean;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new Provider(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.base_url = source["base_url"];
	        this.status = source["status"];
	        this.key_masked = source["key_masked"];
	        this.models_count = source["models_count"];
	        this.monthly_tokens = source["monthly_tokens"];
	        this.avg_latency_ms = source["avg_latency_ms"];
	        this.last_tested_at = source["last_tested_at"];
	        this.error_message = source["error_message"];
	        this.is_custom = source["is_custom"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class TokenTrendPoint {
	    date: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost: number;
	
	    static createFrom(source: any = {}) {
	        return new TokenTrendPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost = source["cost"];
	    }
	}
	export class Stat {
	    label: string;
	    value: string;
	    delta: string;
	    trend: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Stat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.value = source["value"];
	        this.delta = source["delta"];
	        this.trend = source["trend"];
	        this.note = source["note"];
	    }
	}
	export class DashboardData {
	    stats: Stat[];
	    token_trend: TokenTrendPoint[];
	    providers: Provider[];
	    recent_activity: RequestLog[];
	    service_health: ServiceHealth;
	
	    static createFrom(source: any = {}) {
	        return new DashboardData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stats = this.convertValues(source["stats"], Stat);
	        this.token_trend = this.convertValues(source["token_trend"], TokenTrendPoint);
	        this.providers = this.convertValues(source["providers"], Provider);
	        this.recent_activity = this.convertValues(source["recent_activity"], RequestLog);
	        this.service_health = this.convertValues(source["service_health"], ServiceHealth);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DataSettings {
	    log_retention_days: number;
	    storage_path: string;
	
	    static createFrom(source: any = {}) {
	        return new DataSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.log_retention_days = source["log_retention_days"];
	        this.storage_path = source["storage_path"];
	    }
	}
	export class Endpoint {
	    method: string;
	    path: string;
	    desc: string;
	
	    static createFrom(source: any = {}) {
	        return new Endpoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.method = source["method"];
	        this.path = source["path"];
	        this.desc = source["desc"];
	    }
	}
	export class GeneralSettings {
	    launch_at_login: boolean;
	    startup_action: string;
	    menu_bar_item: boolean;
	    close_action: string;
	
	    static createFrom(source: any = {}) {
	        return new GeneralSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.launch_at_login = source["launch_at_login"];
	        this.startup_action = source["startup_action"];
	        this.menu_bar_item = source["menu_bar_item"];
	        this.close_action = source["close_action"];
	    }
	}
	export class LogQuery {
	    start_date: number;
	    end_date: number;
	    provider: string;
	    route_id: string;
	    model: string;
	    status: string;
	    search: string;
	    page: number;
	    page_size: number;
	
	    static createFrom(source: any = {}) {
	        return new LogQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start_date = source["start_date"];
	        this.end_date = source["end_date"];
	        this.provider = source["provider"];
	        this.route_id = source["route_id"];
	        this.model = source["model"];
	        this.status = source["status"];
	        this.search = source["search"];
	        this.page = source["page"];
	        this.page_size = source["page_size"];
	    }
	}
	export class LogQueryResult {
	    logs: RequestLog[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new LogQueryResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.logs = this.convertValues(source["logs"], RequestLog);
	        this.total = source["total"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Model {
	    id: string;
	    provider_id: string;
	    name: string;
	    context_window: number;
	    owned_by: string;
	    active: boolean;
	    latency_ms: number;
	    updated_at: number;
	    created_at: number;
	
	    static createFrom(source: any = {}) {
	        return new Model(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider_id = source["provider_id"];
	        this.name = source["name"];
	        this.context_window = source["context_window"];
	        this.owned_by = source["owned_by"];
	        this.active = source["active"];
	        this.latency_ms = source["latency_ms"];
	        this.updated_at = source["updated_at"];
	        this.created_at = source["created_at"];
	    }
	}
	export class ModelRanking {
	    model: string;
	    provider_name: string;
	    requests: number;
	    tokens: number;
	    cost: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelRanking(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.provider_name = source["provider_name"];
	        this.requests = source["requests"];
	        this.tokens = source["tokens"];
	        this.cost = source["cost"];
	    }
	}
	export class ModelTestResult {
	    ok: boolean;
	    latency_ms: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.latency_ms = source["latency_ms"];
	        this.error = source["error"];
	    }
	}
	
	export class ProviderInput {
	    name: string;
	    base_url: string;
	    upstream_key: string;
	    is_custom: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ProviderInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.base_url = source["base_url"];
	        this.upstream_key = source["upstream_key"];
	        this.is_custom = source["is_custom"];
	    }
	}
	
	export class ProviderTestResult {
	    ok: boolean;
	    latency_ms: number;
	    error?: string;
	    models: string[];
	
	    static createFrom(source: any = {}) {
	        return new ProviderTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.latency_ms = source["latency_ms"];
	        this.error = source["error"];
	        this.models = source["models"];
	    }
	}
	
	export class RouteTarget {
	    id: string;
	    route_id: string;
	    provider_id: string;
	    model_name: string;
	    max_retries: number;
	    hit_count: number;
	    failure_count: number;
	
	    static createFrom(source: any = {}) {
	        return new RouteTarget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.route_id = source["route_id"];
	        this.provider_id = source["provider_id"];
	        this.model_name = source["model_name"];
	        this.max_retries = source["max_retries"];
	        this.hit_count = source["hit_count"];
	        this.failure_count = source["failure_count"];
	    }
	}
	export class RouteCondition {
	    id: string;
	    route_id: string;
	    field: string;
	    operator: string;
	    value: string;
	
	    static createFrom(source: any = {}) {
	        return new RouteCondition(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.route_id = source["route_id"];
	        this.field = source["field"];
	        this.operator = source["operator"];
	        this.value = source["value"];
	    }
	}
	export class Route {
	    id: string;
	    name: string;
	    description: string;
	    enabled: boolean;
	    created_at: number;
	    updated_at: number;
	    conditions: RouteCondition[];
	    targets: RouteTarget[];
	    monthly_hits: number;
	    monthly_savings: number;
	
	    static createFrom(source: any = {}) {
	        return new Route(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.enabled = source["enabled"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.conditions = this.convertValues(source["conditions"], RouteCondition);
	        this.targets = this.convertValues(source["targets"], RouteTarget);
	        this.monthly_hits = source["monthly_hits"];
	        this.monthly_savings = source["monthly_savings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class RouteInput {
	    name: string;
	    description: string;
	    enabled: boolean;
	    conditions: RouteCondition[];
	    targets: RouteTarget[];
	
	    static createFrom(source: any = {}) {
	        return new RouteInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.enabled = source["enabled"];
	        this.conditions = this.convertValues(source["conditions"], RouteCondition);
	        this.targets = this.convertValues(source["targets"], RouteTarget);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class RoutingSettings {
	    default_provider_id: string;
	    default_model: string;
	    auto_retry: boolean;
	    streaming_sse: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RoutingSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_provider_id = source["default_provider_id"];
	        this.default_model = source["default_model"];
	        this.auto_retry = source["auto_retry"];
	        this.streaming_sse = source["streaming_sse"];
	    }
	}
	export class ServerSettings {
	    port: number;
	    bind_address: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.port = source["port"];
	        this.bind_address = source["bind_address"];
	    }
	}
	
	export class Settings {
	    general: GeneralSettings;
	    appearance: AppearanceSettings;
	    routing: RoutingSettings;
	    server: ServerSettings;
	    data: DataSettings;
	    advanced: AdvancedSettings;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.general = this.convertValues(source["general"], GeneralSettings);
	        this.appearance = this.convertValues(source["appearance"], AppearanceSettings);
	        this.routing = this.convertValues(source["routing"], RoutingSettings);
	        this.server = this.convertValues(source["server"], ServerSettings);
	        this.data = this.convertValues(source["data"], DataSettings);
	        this.advanced = this.convertValues(source["advanced"], AdvancedSettings);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	export class UsageStats {
	    token_stats: Stat[];
	    token_trend_30: TokenTrendPoint[];
	    providers: ProviderShare[];
	    model_ranking: ModelRanking[];
	    log_stats: Stat[];
	    logs: RequestLog[];
	    log_total: number;
	
	    static createFrom(source: any = {}) {
	        return new UsageStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token_stats = this.convertValues(source["token_stats"], Stat);
	        this.token_trend_30 = this.convertValues(source["token_trend_30"], TokenTrendPoint);
	        this.providers = this.convertValues(source["providers"], ProviderShare);
	        this.model_ranking = this.convertValues(source["model_ranking"], ModelRanking);
	        this.log_stats = this.convertValues(source["log_stats"], Stat);
	        this.logs = this.convertValues(source["logs"], RequestLog);
	        this.log_total = source["log_total"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

